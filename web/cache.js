// Only public transcript data lives here. Invitation credentials stay in memory.
export class PublicCache {
  static async open(namespace) {
    const db = await new Promise((resolve, reject) => {
      const r = indexedDB.open("spr-public-work-v1", 1);
      r.onupgradeneeded = () => r.result.createObjectStore("records");
      r.onsuccess = () => resolve(r.result);
      r.onerror = () => reject(new Error("Browser storage is unavailable; use the native client"));
      r.onblocked = () => reject(new Error("Close other ceremony tabs to enable storage"));
    });
    db.onversionchange = () => db.close();
    return new PublicCache(db, namespace + ":");
  }
  constructor(db, prefix) { this.db = db; this.prefix = prefix; }
  operation(mode, action) {
    return new Promise((resolve, reject) => {
      const tx = this.db.transaction("records", mode);
      const request = action(tx.objectStore("records"));
      tx.oncomplete = () => resolve(request?.result);
      tx.onabort = () => reject(new Error(`Cannot save public work: ${tx.error?.message || tx.error?.name || "storage transaction aborted"}. Free browser storage or use the native client.`));
      tx.onerror = () => {}; // onabort reports the final transaction result.
    });
  }
  get(key) { return this.operation("readonly", s => s.get(this.prefix + key)); }
  put(key, value) { return this.operation("readwrite", s => s.put(value, this.prefix + key)); }
  delete(key) { return this.operation("readwrite", s => s.delete(this.prefix + key)); }
  async clearParts(keepHash) {
    const prefix = this.prefix + "part:";
    await this.operation("readwrite", s => {
      const r = s.openCursor(IDBKeyRange.bound(prefix, prefix + "\uffff"));
      r.onsuccess = () => {
        const cursor = r.result;
        if (!cursor) return;
        if (!keepHash || !String(cursor.key).startsWith(prefix + keepHash + ":")) cursor.delete();
        cursor.continue();
      };
    });
  }
  close() { this.db.close(); }
}
