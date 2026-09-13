var __defProp = Object.defineProperty;
var __name = (target, value) => __defProp(target, "name", { value, configurable: true });

// src/index.ts
var VERSION = "0.1.0";
var MAX_BODY_BYTES = 10 * 1024 * 1024;
var MAX_FILE_BYTES = 5 * 1024 * 1024;
var MAX_FILES = 200;
var PROJECT_ID = /^[a-z0-9][a-z0-9-]{0,62}$/;
var TOKEN_BYTES = 32;
var HttpError = class extends Error {
  constructor(status, code, message) {
    super(message);
    this.status = status;
    this.code = code;
  }
  status;
  code;
  static {
    __name(this, "HttpError");
  }
};
var index_default = {
  async fetch(request, env) {
    const url = new URL(request.url);
    try {
      if (url.pathname === "/robots.txt") {
        return new Response("User-agent: *\nDisallow: /\n", {
          headers: { "Content-Type": "text/plain; charset=utf-8" }
        });
      }
      if (url.pathname === "/v" || url.pathname.startsWith("/v/")) {
        return await viewer(url, request, env);
      }
      if (url.pathname.startsWith("/api/v1/")) {
        return await api(url, request, env);
      }
      return new Response("Not Found", { status: 404 });
    } catch (err) {
      if (err instanceof HttpError) {
        return json({ error: err.code, message: err.message }, err.status);
      }
      console.error("orch-cloud: unhandled", err);
      return json({ error: "internal", message: "internal error" }, 500);
    }
  }
};
async function api(url, request, env) {
  const parts = url.pathname.slice("/api/v1/".length).split("/");
  const method = request.method;
  if (parts.length === 1 && parts[0] === "health") {
    allow(method, "GET");
    return json({ ok: true, version: VERSION, api: 1 });
  }
  if (parts.length === 1 && parts[0] === "whoami") {
    allow(method, "GET");
    await requireAdmin(request, env);
    return json({ role: "admin" });
  }
  if (parts.length === 1 && parts[0] === "projects") {
    allow(method, "POST");
    await requireAdmin(request, env);
    return createProject(request, env);
  }
  if (parts[0] === "projects" && parts.length >= 2) {
    const id = parts[1];
    if (!PROJECT_ID.test(id)) {
      throw new HttpError(400, "invalid", `project id ${JSON.stringify(id)} is not ^[a-z0-9][a-z0-9-]{0,62}$`);
    }
    if (parts.length === 2) {
      allow(method, "DELETE");
      await requireAdmin(request, env);
      return deleteProject(id, env);
    }
    if (parts.length === 3 && parts[2] === "publish-token") {
      allow(method, "POST");
      await requireAdmin(request, env);
      return rotatePublish(id, env);
    }
    if (parts.length === 3 && parts[2] === "view-token") {
      allow(method, "POST");
      const project = await requirePublisher(request, env, id);
      return rotateView(project, env);
    }
    if (parts.length === 3 && parts[2] === "site") {
      allow(method, "PUT");
      const project = await requirePublisher(request, env, id);
      return uploadSite(project, request, env);
    }
  }
  throw new HttpError(404, "not_found", `no API route ${url.pathname}`);
}
__name(api, "api");
function allow(method, want) {
  if (method !== want) {
    throw new HttpError(405, "method_not_allowed", `use ${want}`);
  }
}
__name(allow, "allow");
async function createProject(request, env) {
  const body = await readJSON(request);
  if (typeof body.id !== "string" || !PROJECT_ID.test(body.id)) {
    throw new HttpError(400, "invalid", 'body must be {"id": "<project id matching ^[a-z0-9][a-z0-9-]{0,62}$>"}');
  }
  if (await env.ORCH.get(projectKey(body.id))) {
    throw new HttpError(409, "conflict", `project ${body.id} already exists \u2014 rotate its publish token instead`);
  }
  const publishToken = newToken();
  const viewToken = newToken();
  const now = (/* @__PURE__ */ new Date()).toISOString();
  const project = {
    id: body.id,
    publish_hash: await sha256Hex(publishToken),
    view_hash: await sha256Hex(viewToken),
    created_at: now,
    updated_at: now,
    site_version: 0
  };
  await env.ORCH.put(viewKey(project.view_hash), project.id);
  await env.ORCH.put(projectKey(project.id), JSON.stringify(project));
  return json({ id: project.id, publish_token: publishToken, view_token: viewToken }, 201);
}
__name(createProject, "createProject");
async function deleteProject(id, env) {
  const project = await getProject(id, env);
  if (!project) {
    throw new HttpError(404, "not_found", `no project ${id}`);
  }
  await env.ORCH.delete(projectKey(id));
  await env.ORCH.delete(viewKey(project.view_hash));
  await env.ORCH.delete(manifestKey(id));
  return new Response(null, { status: 204 });
}
__name(deleteProject, "deleteProject");
async function rotatePublish(id, env) {
  const project = await getProject(id, env);
  if (!project) {
    throw new HttpError(404, "not_found", `no project ${id}`);
  }
  const token = newToken();
  project.publish_hash = await sha256Hex(token);
  project.updated_at = (/* @__PURE__ */ new Date()).toISOString();
  await env.ORCH.put(projectKey(id), JSON.stringify(project));
  return json({ publish_token: token });
}
__name(rotatePublish, "rotatePublish");
async function rotateView(project, env) {
  const token = newToken();
  const oldHash = project.view_hash;
  project.view_hash = await sha256Hex(token);
  project.updated_at = (/* @__PURE__ */ new Date()).toISOString();
  await env.ORCH.put(viewKey(project.view_hash), project.id);
  await env.ORCH.put(projectKey(project.id), JSON.stringify(project));
  await env.ORCH.delete(viewKey(oldHash));
  return json({ view_token: token });
}
__name(rotateView, "rotateView");
async function uploadSite(project, request, env) {
  const body = await readJSON(request);
  if (typeof body.digest !== "string" || body.digest === "") {
    throw new HttpError(400, "invalid", "body.digest must be a non-empty string");
  }
  if (typeof body.files !== "object" || body.files === null || Array.isArray(body.files)) {
    throw new HttpError(400, "invalid", "body.files must be an object of path \u2192 base64");
  }
  const entries = Object.entries(body.files);
  if (entries.length === 0 || entries.length > MAX_FILES) {
    throw new HttpError(400, "invalid", `a site has 1 to ${MAX_FILES} files, got ${entries.length}`);
  }
  if (!body.files["index.html"]) {
    throw new HttpError(400, "invalid", "a site must contain index.html");
  }
  const current = await getManifest(project.id, env);
  if (current && current.digest === body.digest) {
    return json({ changed: false, version: current.version });
  }
  const decoded = [];
  for (const [path, value] of entries) {
    validatePath(path);
    if (typeof value !== "string") {
      throw new HttpError(400, "invalid", `files[${JSON.stringify(path)}] must be a base64 string`);
    }
    const bytes = fromBase64(value, path);
    if (bytes.byteLength > MAX_FILE_BYTES) {
      throw new HttpError(413, "too_large", `${path} is ${bytes.byteLength} bytes; the limit is ${MAX_FILE_BYTES}`);
    }
    decoded.push({ path, bytes });
  }
  const manifest = { version: (current?.version ?? 0) + 1, digest: body.digest, files: {} };
  for (const { path, bytes } of decoded) {
    const blob = await sha256Hex(bytes);
    await env.ORCH.put(blobKey(blob), bytes);
    manifest.files[path] = { blob, type: contentType(path), size: bytes.byteLength };
  }
  await env.ORCH.put(manifestKey(project.id), JSON.stringify(manifest));
  project.site_version = manifest.version;
  project.updated_at = (/* @__PURE__ */ new Date()).toISOString();
  await env.ORCH.put(projectKey(project.id), JSON.stringify(project));
  return json({ changed: true, version: manifest.version });
}
__name(uploadSite, "uploadSite");
async function viewer(url, request, env) {
  if (request.method !== "GET" && request.method !== "HEAD") {
    return notFound();
  }
  const rest = url.pathname.slice("/v/".length);
  const slash = rest.indexOf("/");
  const token = slash === -1 ? rest : rest.slice(0, slash);
  if (!/^[0-9a-f]{64}$/.test(token)) {
    return notFound();
  }
  const projectId = await env.ORCH.get(viewKey(await sha256Hex(token)));
  if (!projectId) {
    return notFound();
  }
  if (slash === -1) {
    return new Response(null, {
      status: 308,
      headers: { Location: `/v/${token}/`, ...viewerHeaders("") }
    });
  }
  let path = rest.slice(slash + 1);
  if (path === "") {
    path = "index.html";
  }
  const manifest = await getManifest(projectId, env);
  const entry = manifest?.files[path];
  if (!entry) {
    return notFound();
  }
  const bytes = await env.ORCH.get(blobKey(entry.blob), "arrayBuffer");
  if (!bytes) {
    console.error(`orch-cloud: manifest for ${projectId} names missing blob ${entry.blob}`);
    return notFound();
  }
  return new Response(request.method === "HEAD" ? null : bytes, {
    headers: { "Content-Type": entry.type, ...viewerHeaders(path) }
  });
}
__name(viewer, "viewer");
function viewerHeaders(path) {
  return {
    "Referrer-Policy": "no-referrer",
    "X-Robots-Tag": "noindex, nofollow",
    "X-Content-Type-Options": "nosniff",
    "Cache-Control": cacheControl(path)
  };
}
__name(viewerHeaders, "viewerHeaders");
function cacheControl(path) {
  if (path === "index.html" || path === "data.json" || path === "data.js" || path === "") {
    return "no-store";
  }
  if (path.startsWith("assets/")) {
    return "public, max-age=31536000, immutable";
  }
  return "no-cache";
}
__name(cacheControl, "cacheControl");
function notFound() {
  return new Response("Not Found", { status: 404, headers: viewerHeaders("") });
}
__name(notFound, "notFound");
function bearer(request) {
  const header = request.headers.get("Authorization") ?? "";
  const match = /^Bearer\s+(\S+)$/.exec(header);
  return match ? match[1] : "";
}
__name(bearer, "bearer");
async function requireAdmin(request, env) {
  if (!env.ADMIN_TOKEN) {
    throw new HttpError(
      401,
      "unauthorized",
      "this Worker has no ADMIN_TOKEN secret \u2014 run `npx wrangler secret put ADMIN_TOKEN`"
    );
  }
  const supplied = bearer(request);
  if (!supplied || !timingSafeEqual(await sha256Hex(supplied), await sha256Hex(env.ADMIN_TOKEN))) {
    throw new HttpError(401, "unauthorized", "missing or wrong admin token");
  }
}
__name(requireAdmin, "requireAdmin");
async function requirePublisher(request, env, id) {
  const supplied = bearer(request);
  const project = await getProject(id, env);
  if (!supplied || !project || !timingSafeEqual(await sha256Hex(supplied), project.publish_hash)) {
    throw new HttpError(401, "unauthorized", `missing or wrong publish token for project ${id}`);
  }
  return project;
}
__name(requirePublisher, "requirePublisher");
function timingSafeEqual(a, b) {
  if (a.length !== b.length) {
    return false;
  }
  let diff = 0;
  for (let i = 0; i < a.length; i++) {
    diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  }
  return diff === 0;
}
__name(timingSafeEqual, "timingSafeEqual");
var projectKey = /* @__PURE__ */ __name((id) => `project:${id}`, "projectKey");
var viewKey = /* @__PURE__ */ __name((hash) => `view:${hash}`, "viewKey");
var manifestKey = /* @__PURE__ */ __name((id) => `manifest:${id}`, "manifestKey");
var blobKey = /* @__PURE__ */ __name((hash) => `blob:${hash}`, "blobKey");
async function getProject(id, env) {
  return env.ORCH.get(projectKey(id), "json");
}
__name(getProject, "getProject");
async function getManifest(id, env) {
  return env.ORCH.get(manifestKey(id), "json");
}
__name(getManifest, "getManifest");
async function readJSON(request) {
  const declared = Number(request.headers.get("Content-Length") ?? "0");
  if (declared > MAX_BODY_BYTES) {
    throw new HttpError(413, "too_large", `request body over ${MAX_BODY_BYTES} bytes`);
  }
  const text = await request.text();
  if (text.length > MAX_BODY_BYTES) {
    throw new HttpError(413, "too_large", `request body over ${MAX_BODY_BYTES} bytes`);
  }
  try {
    const value = JSON.parse(text);
    if (typeof value !== "object" || value === null || Array.isArray(value)) {
      throw new Error("not an object");
    }
    return value;
  } catch {
    throw new HttpError(400, "invalid", "request body must be a JSON object");
  }
}
__name(readJSON, "readJSON");
function validatePath(path) {
  const segments = path.split("/");
  if (path === "" || path.startsWith("/") || path.includes("\\") || segments.some((s) => s === "" || s === "." || s === "..")) {
    throw new HttpError(400, "invalid", `file path ${JSON.stringify(path)} must be relative with no empty, "." or ".." segment`);
  }
}
__name(validatePath, "validatePath");
function fromBase64(value, path) {
  let binary;
  try {
    binary = atob(value);
  } catch {
    throw new HttpError(400, "invalid", `files[${JSON.stringify(path)}] is not valid base64`);
  }
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}
__name(fromBase64, "fromBase64");
var TYPES = {
  html: "text/html; charset=utf-8",
  js: "text/javascript; charset=utf-8",
  css: "text/css; charset=utf-8",
  json: "application/json; charset=utf-8",
  svg: "image/svg+xml",
  png: "image/png",
  ico: "image/x-icon",
  txt: "text/plain; charset=utf-8",
  webmanifest: "application/manifest+json",
  woff2: "font/woff2"
};
function contentType(path) {
  const dot = path.lastIndexOf(".");
  const ext = dot === -1 ? "" : path.slice(dot + 1).toLowerCase();
  return TYPES[ext] ?? "application/octet-stream";
}
__name(contentType, "contentType");
function newToken() {
  const bytes = new Uint8Array(TOKEN_BYTES);
  crypto.getRandomValues(bytes);
  return hex(bytes);
}
__name(newToken, "newToken");
async function sha256Hex(input) {
  const data = typeof input === "string" ? new TextEncoder().encode(input) : input;
  return hex(new Uint8Array(await crypto.subtle.digest("SHA-256", data)));
}
__name(sha256Hex, "sha256Hex");
function hex(bytes) {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}
__name(hex, "hex");
function json(value, status = 200) {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store" }
  });
}
__name(json, "json");
export {
  index_default as default
};
//# sourceMappingURL=index.js.map
