(function () {
  if (!modules.artifacts || !modules.artifacts.open || !modules.artifacts.fetch) return;
  class FileRef {
    constructor(ref) {
      if (!ref || typeof ref !== "object") throw new Error("File reference required");
      this._id = ref.reference_id || ref.artifact_id || "";
      this._currentId = ref.artifact_id || "";
      this._source = {module: ref.module || ref.source_module, method: ref.method || ref.source_fetch_method, external_id: ref.external_id || ref.source_external_id};
      this._pending = null;
    }
    get id() { return this._id; }
    get currentId() { return this._currentId; }
    get source() { return Object.assign({}, this._source); }
    async handle() {
      if (this._pending) return this._pending;
      this._pending = this._open();
      try { return await this._pending; }
      finally { this._pending = null; }
    }
    async _open() {
      if (!this._id) {
        const result = await modules.artifacts.fetch(this._source);
        if (!result.artifacts || result.artifacts.length !== 1) throw new Error("One file required");
        const file = result.artifacts[0];
        this._id = file.reference_id || file.artifact_id;
        this._currentId = file.artifact_id;
      }
      const handle = await modules.artifacts.open({artifact_id: this._id});
      this._currentId = handle.artifact_id;
      return handle;
    }
    async call(method, options) {
      if (!modules.files || !Object.prototype.hasOwnProperty.call(modules.files, method) || typeof modules.files[method] !== "function") throw new Error("File method unavailable");
      const handle = await this.handle();
      return modules.files[method](Object.assign({}, options || {}, {handle_id: handle.handle_id}));
    }
    toJSON() {
      return Object.assign({}, this._source, {artifact_id: this._id});
    }
  }
  modules.artifacts.file = function (ref) { return new FileRef(ref); };
})();
