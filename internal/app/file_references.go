package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/complynx/loom/internal/artifacts"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/sandbox"
	"github.com/complynx/loom/internal/store"
)

const fileRefreshTimeout = 30 * time.Second
const fileRefreshPoll = 50 * time.Millisecond

type fileRefreshKey struct{}

// refreshImportPolicy checks immutable identity before downloading or consuming quota.
func refreshImportPolicy(
	ctx context.Context,
	source modules.ArtifactSource,
	d modules.ArtifactDescriptor,
) (string, bool, error) {
	original, refreshing := ctx.Value(fileRefreshKey{}).(domain.Artifact)
	if !refreshing {
		return "", source.ExternalDisclosure, nil
	}
	if source.ArtifactCount != 1 || source.ServiceID != original.SourceServiceID ||
		source.Module != original.SourceModule ||
		source.Method != original.SourceFetchMethod ||
		d.ExternalID != original.SourceExternalID ||
		d.FetchMethod != original.SourceFetchMethod ||
		!strings.EqualFold(d.SHA256, original.SHA256) ||
		d.SizeBytes != original.Size ||
		d.MIMEType != original.MIME {
		return "", false, domain.Conflict("file_version_changed")
	}
	return original.ID, source.ExternalDisclosure && original.ExternalDisclosureAllowed, nil
}

func (t runtimeTools) fileBroker(run domain.Run) sandbox.Broker {
	return t.registry.BrokerLive(func(ctx context.Context) (modules.Principal, error) {
		p, credential, err := t.fresh(ctx, run)
		value := modulePrincipal(p, credential, run.SettingsSnapshot.Version)
		value.UserID = run.OwnerID
		return value, err
	}, run.ID, func(ctx context.Context) error { return t.bridge.guard(ctx, run) })
}

func (t runtimeTools) authorizeFileOrigin(ctx context.Context, run domain.Run, original domain.Artifact) error {
	p, token, err := t.fresh(ctx, run)
	if err != nil {
		return err
	}
	if !p.Has("artifacts.read") || !p.Has("chat.read") {
		return domain.Forbidden()
	}
	mp := modulePrincipal(p, token, run.SettingsSnapshot.Version)
	mp.UserID = run.OwnerID
	descriptor, err := t.registry.Describe(ctx, mp, original.SourceModule, original.SourceMethod)
	if err != nil {
		return err
	}
	if descriptor.ServiceID != original.SourceServiceID {
		return domain.Forbidden()
	}
	fetch, err := t.registry.Describe(ctx, mp, original.SourceModule, original.SourceFetchMethod)
	if err != nil {
		return err
	}
	if fetch.ServiceID != original.SourceServiceID || fetch.Method.Effect != builtinReadMethod {
		return domain.Forbidden()
	}
	if !descriptor.Method.ExternalDisclosure || !fetch.Method.ExternalDisclosure {
		return artifacts.ErrForbidden
	}
	return nil
}

// resolveArtifact reuses live bytes or refetches the exact source version under current rights.
func (t runtimeTools) resolveArtifact(
	ctx context.Context,
	run domain.Run,
	p domain.Principal,
	aid string,
) (domain.Artifact, error) {
	ctx, cancel := context.WithTimeout(ctx, fileRefreshTimeout)
	defer cancel()
	owner := artifacts.Principal{OwnerID: run.OwnerID, OrgID: run.OrgID}
	live, err := t.cachedFile(ctx, owner, aid)
	if err == nil && live.SourceReferenceID == "" {
		return live, nil
	}
	if err != nil && !missingFile(err) {
		return domain.Artifact{}, err
	}
	for {
		ref, refErr := t.repository.FileReference(ctx, p, aid)
		if refErr != nil {
			return domain.Artifact{}, refErr
		}
		if authErr := t.authorizeFileOrigin(ctx, run, ref.Original); authErr != nil {
			return domain.Artifact{}, authErr
		}
		current, currentErr := t.cachedFile(ctx, owner, ref.CurrentID)
		if currentErr == nil {
			return current, nil
		}
		if !missingFile(currentErr) {
			return domain.Artifact{}, currentErr
		}
		lease, claimed, claimErr := t.repository.ClaimFileRefresh(ctx, ref)
		if claimErr != nil {
			return domain.Artifact{}, claimErr
		}
		if claimed {
			return t.refreshArtifact(ctx, run, ref, lease)
		}
		timer := time.NewTimer(fileRefreshPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return domain.Artifact{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (t runtimeTools) performFileRefresh(
	ctx context.Context,
	run domain.Run,
	ref store.FileReference,
	lease string,
) (domain.Artifact, error) {
	defer func() {
		work, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = t.repository.ReleaseFileRefresh(work, ref.Original.ID, lease)
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go t.keepFileRefresh(ctx, cancel, done, ref.Original.ID, lease)
	args, err := json.Marshal(map[string]string{"external_id": ref.Original.SourceExternalID})
	if err != nil {
		return domain.Artifact{}, err
	}
	refreshCtx := context.WithValue(ctx, fileRefreshKey{}, ref.Original)
	raw, err := t.fileBroker(run).Call(refreshCtx, ref.Original.SourceModule, ref.Original.SourceFetchMethod, args)
	if err != nil {
		return domain.Artifact{}, err
	}
	var result struct {
		Artifacts []domain.Artifact `json:"artifacts"`
	}
	if json.Unmarshal(raw, &result) != nil || len(result.Artifacts) != 1 {
		return domain.Artifact{}, artifacts.ErrInvalid
	}
	fresh := result.Artifacts[0]
	if err = t.authorizeFileOrigin(ctx, run, ref.Original); err != nil {
		return domain.Artifact{}, err
	}
	if err = t.bridge.guard(ctx, run); err != nil {
		return domain.Artifact{}, err
	}
	if err = t.repository.CompleteFileRefresh(ctx, ref, lease, fresh.ID); err != nil {
		return domain.Artifact{}, err
	}
	return fresh, nil
}

func (t runtimeTools) resolveFileHandle(
	ctx context.Context,
	run domain.Run,
	p domain.Principal,
	hid string,
) (domain.Artifact, error) {
	handle, err := t.repository.LoadArtifactHandle(ctx, p, hid)
	if err == nil {
		if handle.RunID != run.ID {
			return domain.Artifact{}, domain.NotFound()
		}
		return t.resolveArtifact(ctx, run, p, handle.ArtifactID)
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return domain.Artifact{}, err
	}
	aid, err := t.repository.FileHandleReference(ctx, p, run.ID, hid)
	if err != nil {
		return domain.Artifact{}, err
	}
	// An expired handle is only refreshable when backed by a durable external reference.
	if _, err = t.repository.FileReference(ctx, p, aid); err != nil {
		return domain.Artifact{}, err
	}
	return t.resolveArtifact(ctx, run, p, aid)
}

func missingFile(err error) bool {
	return errors.Is(err, artifacts.ErrNotFound) || errors.Is(err, os.ErrNotExist)
}

func (t runtimeTools) cachedFile(ctx context.Context, p artifacts.Principal, aid string) (domain.Artifact, error) {
	item, reader, err := t.files.Download(ctx, p, aid)
	if err != nil {
		return domain.Artifact{}, err
	}
	if err = reader.Close(); err != nil {
		return domain.Artifact{}, err
	}
	return item, nil
}

func (t runtimeTools) refreshArtifact(
	ctx context.Context,
	run domain.Run,
	ref store.FileReference,
	lease string,
) (domain.Artifact, error) {
	started := time.Now()
	item, err := t.performFileRefresh(ctx, run, ref, lease)
	if t.observer != nil {
		t.observer.Operation(ctx, "artifact_refresh", operationOutcome(err), run.ID, started)
	}
	return item, err
}

func (t runtimeTools) keepFileRefresh(
	ctx context.Context,
	cancel context.CancelFunc,
	done <-chan struct{},
	referenceID, lease string,
) {
	const heartbeat = 500 * time.Millisecond
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			work, stop := context.WithTimeout(ctx, heartbeat)
			err := t.repository.RenewFileRefresh(work, referenceID, lease)
			stop()
			if err != nil {
				cancel()
				return
			}
		}
	}
}
