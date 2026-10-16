<a id="docker-discovery-и-модули-v1"></a>

# Module integration guide

Use this guide to expose a service to Loom through Docker discovery and the v1 module protocol. Commands run from the repository root unless stated otherwise. Start with [Discovery](modules/discovery.md), then check [the manifest example](../examples/manifest.json) and [schema](../schemas/manifest.schema.json). See [Contributing](../CONTRIBUTING.md) for the code map and development workflow.

<a id="регистрация-одной-строкой"></a>

## Register a service

[Discovery](modules/discovery.md#register-a-service) explains the container label, trusted network, manifest refresh and replica rules.

## Caller registration

[Caller registration](modules/discovery.md#caller-registration) defines service permissions and supported incoming authentication. Caller authority is separate from user delegation.

<a id="статические-и-динамические-модули"></a>

## Static and dynamic modules

[Static and dynamic modules](modules/discovery.md#static-and-dynamic-modules) covers permission checks, discovery responses, caching and method fields.

## Developer requirement: usable method help

[Authoring help](modules/authoring-help.md) explains what each method description must teach and how to use bounded help cards.

## Agent-facing prose

[Agent-facing prose](modules/authoring-help.md#agent-facing-prose) gives language and clarity requirements for module descriptions and errors.

## RPC

[Calls and confirmation](modules/calls-confirmation.md#rpc) defines request/response shapes, durable operation IDs, status lookup and current recovery limits.

<a id="подтверждения"></a>

## Confirmation

[Confirmation](modules/calls-confirmation.md#confirmation) explains preparation, approval binding and user reply modes.

## JS API

[Scripts and files](modules/scripts-files.md#js-api) explains remote read calls, builtin modules and the separate durable action tool.

<a id="артефакты"></a>

## Artifacts

[Artifacts](modules/scripts-files.md#artifacts) explains service downloads, temporary handles, file references and model attachments. Use the [acceptance service](acceptance-service.md) to exercise the protocol locally.
