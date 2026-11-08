"use strict";

const fileInput = document.querySelector("#file-input");
const uploadForm = document.querySelector("#upload-form");
const uploadButton = document.querySelector("#upload-button");
const uploadMessage = document.querySelector("#upload-message");
const fileLabel = document.querySelector("#file-label");
const fileHint = document.querySelector("#file-hint");
const dropzone = document.querySelector("#dropzone");
const statusForm = document.querySelector("#status-form");
const documentIdInput = document.querySelector("#document-id");
const statusMessage = document.querySelector("#status-message");
const statusEmpty = document.querySelector("#status-empty");
const statusResult = document.querySelector("#status-result");
const refreshButton = document.querySelector("#refresh-button");
const pollIndicator = document.querySelector("#poll-indicator");
const chunkMessage = document.querySelector("#chunk-message");
const chunkViewer = document.querySelector("#chunk-viewer");
const chunkList = document.querySelector("#chunk-list");
const chunkPrevious = document.querySelector("#chunks-previous");
const chunkNext = document.querySelector("#chunks-next");
const pdfFrame = document.querySelector("#pdf-frame");
const pdfOpen = document.querySelector("#pdf-open");
const searchForm = document.querySelector("#search-form");
const searchButton = document.querySelector("#search-button");
const answerButton = document.querySelector("#answer-button");
const searchMessage = document.querySelector("#search-message");
const searchResults = document.querySelector("#search-results");
const answerResult = document.querySelector("#answer-result");
const answerText = document.querySelector("#answer-text");
const chunkPageSize = 20;
const terminalStatuses = new Set(["COMPLETED", "FAILED"]);
let selectedFile = null;
let timer = null;
let requestVersion = 0;
let trackedId = "";
let chunkOffset = 0;
let chunkRequest = 0;
let accessToken = "";
let activeProject = "";
let workspaceVersion = 0;
let sessionVersion = 0;
let projects = [];
let pdfObjectURL = "";
let pdfRequest = 0;
let requestedPDFPage = null;
const accountMessage = document.querySelector("#account-message");
const dialogMessage = document.querySelector("#dialog-message");
const workspaceDialog = document.querySelector("#workspace-dialog");
const authForm = document.querySelector("#auth-form");
const accountControls = document.querySelector("#account-controls");
const projectForm = document.querySelector("#project-form");
const projectControls = document.querySelector("#project-controls");
const projectList = document.querySelector("#project-list");
const documentSelect = document.querySelector("#project-documents");

function accountNotice(text, kind = "") {
  message(accountMessage, text, kind);
  message(dialogMessage, text, kind);
}

function openWorkspaceDialog(section) {
  document.querySelector("#dialog-title").textContent = section === "account" ? "Account" : section === "create" ? "Create a project" : "Project settings";
  authForm.hidden = section !== "account" || Boolean(accessToken);
  accountControls.hidden = section !== "account" || !accessToken;
  projectForm.hidden = section !== "create";
  projectControls.hidden = section !== "project";
  message(dialogMessage, "");
  if (!workspaceDialog.open) workspaceDialog.showModal();
}

function updateProjectList() {
  projectList.replaceChildren();
  for (const project of projects) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "project-link";
    button.textContent = project.name;
    button.setAttribute("aria-pressed", String(project.id === activeProject));
    button.addEventListener("click", () => selectProject(project.id));
    projectList.append(button);
  }
  document.querySelector("#project-empty").hidden = projects.length > 0;
  const project = projects.find(item => item.id === activeProject);
  document.querySelector("#active-project-label").textContent = project ? project.name : "No project selected";
  document.querySelector("#project-settings-button").disabled = !project;
}

function resetWorkspace() {
  workspaceVersion += 1;
  stopTracking();
  chunkRequest += 1;
  pdfRequest += 1;
  requestedPDFPage = null;
  trackedId = "";
  documentIdInput.value = "";
  document.querySelector("#search-document").value = "";
  document.querySelector("#search-query").value = "";
  fileInput.value = "";
  selectFile(null);
  message(statusMessage, "");
  message(chunkMessage, "");
  message(searchMessage, "");
  statusResult.hidden = true;
  statusEmpty.hidden = false;
  chunkViewer.hidden = true;
  chunkList.replaceChildren();
  searchResults.replaceChildren();
  answerResult.hidden = true;
  answerText.replaceChildren();
  pdfFrame.removeAttribute("src");
  pdfOpen.removeAttribute("href");
  if (pdfObjectURL) URL.revokeObjectURL(pdfObjectURL);
  pdfObjectURL = "";
}

function logout() {
  sessionVersion += 1;
  accessToken = "";
  activeProject = "";
  projects = [];
  resetWorkspace();
  updateProjectList();
  documentSelect.replaceChildren(new Option("Select a document", ""));
  document.querySelector("#sidebar-projects").hidden = true;
  document.querySelector("#create-project-button").hidden = true;
  document.querySelector("#project-settings-button").hidden = true;
  document.querySelector("#account-button").textContent = "Log in / Register";
  authForm.hidden = false;
  accountControls.hidden = true;
  projectForm.hidden = true;
  projectControls.hidden = true;
  document.querySelector("#auth-password").value = "";
  if (workspaceDialog.open) workspaceDialog.close();
}

async function authenticatedFetch(path, options = {}) {
  if (!accessToken) throw new Error("Please log in first.");
  const token = accessToken;
  const headers = new Headers(options.headers || {});
  headers.set("Authorization", `Bearer ${token}`);
  const response = await fetch(path, { ...options, headers, cache: "no-store" });
  if (response.status === 401 && token === accessToken) {
    logout();
    accountNotice("Your login expired. Please log in again.", "error");
    throw new Error("Your login expired. Please log in again.");
  }
  return response;
}

function projectPath(path) {
  if (!activeProject) throw new Error("Select a project first.");
  return `/api/v1/projects/${encodeURIComponent(activeProject)}${path}`;
}

async function loadProjects() {
  const version = sessionVersion;
  const data = await responseJSON(await authenticatedFetch("/api/v1/projects"));
  if (version !== sessionVersion) return;
  projects = data.projects;
  updateProjectList();
}

async function loadProjectDocuments() {
  documentSelect.replaceChildren(new Option("Select a document", ""));
  if (!activeProject) return;
  const version = workspaceVersion;
  const data = await responseJSON(await authenticatedFetch(projectPath("/documents")));
  if (version !== workspaceVersion) return;
  for (const item of data.documents) documentSelect.add(new Option(`${item.filename} · ${item.status}`, item.id));
}

document.querySelector("#account-button").addEventListener("click", () => openWorkspaceDialog("account"));
document.querySelector("#create-project-button").addEventListener("click", () => openWorkspaceDialog("create"));
document.querySelector("#project-settings-button").addEventListener("click", () => openWorkspaceDialog("project"));
document.querySelector("#close-dialog").addEventListener("click", () => workspaceDialog.close());

authForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  const version = sessionVersion;
  const action = event.submitter.value;
  const body = JSON.stringify({ email: document.querySelector("#auth-email").value, password: document.querySelector("#auth-password").value });
  try {
    const response = await fetch(`/api/v1/auth/${action}`, { method: "POST", headers: { "Content-Type": "application/json" }, body });
    if (version !== sessionVersion) return;
    document.querySelector("#auth-password").value = "";
    if (action === "register" && response.ok) {
      accountNotice("Account created. Log in to continue.", "success");
      return;
    }
    const data = await responseJSON(response);
    accessToken = data.access_token;
    if (!accessToken) throw new Error("The server returned no access token.");
    await loadProjects();
    if (version !== sessionVersion) return;
    document.querySelector("#sidebar-projects").hidden = false;
    document.querySelector("#create-project-button").hidden = false;
    document.querySelector("#project-settings-button").hidden = false;
    document.querySelector("#account-button").textContent = "Account";
    workspaceDialog.close();
    accountNotice("Logged in. Select or create a project.");
  } catch (error) {
    if (version !== sessionVersion) return;
    accessToken = "";
    accountNotice(error.message, "error");
  }
});

document.querySelector("#logout-button").addEventListener("click", () => {
  logout();
  accountNotice("Logged out. The access token was removed from this browser.");
});

projectForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  const version = sessionVersion;
  try {
    const name = document.querySelector("#project-name").value.trim();
    const project = await responseJSON(await authenticatedFetch("/api/v1/projects", {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name }),
    }));
    if (version !== sessionVersion) return;
    await loadProjects();
    if (version !== sessionVersion) return;
    document.querySelector("#project-name").value = "";
    workspaceDialog.close();
    await selectProject(project.id);
  } catch (error) { if (version === sessionVersion) accountNotice(error.message, "error"); }
});

async function selectProject(projectID) {
  activeProject = projectID;
  resetWorkspace();
  updateProjectList();
  const version = workspaceVersion;
  try {
    await loadProjectDocuments();
    if (version === workspaceVersion) accountNotice("Project selected.");
  } catch (error) { if (version === workspaceVersion) accountNotice(error.message, "error"); }
}

documentSelect.addEventListener("change", () => {
  if (!documentSelect.value) return;
  track(documentSelect.value);
  workspaceDialog.close();
  document.querySelector("#status").scrollIntoView();
});
document.querySelector("#member-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const version = workspaceVersion;
  try {
    const response = await authenticatedFetch(projectPath("/members"), {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email: document.querySelector("#member-email").value }),
    });
    if (!response.ok) await responseJSON(response);
    if (version !== workspaceVersion) return;
    document.querySelector("#member-email").value = "";
    accountNotice("Member added to this project.", "success");
  } catch (error) { if (version === workspaceVersion) accountNotice(error.message, "error"); }
});

function message(element, text, kind = "") {
  element.textContent = text;
  element.className = `message ${kind}`;
}

function selectFile(file) {
  selectedFile = null;
  message(uploadMessage, "");
  if (!file) {
    fileLabel.textContent = "Drop your PDF here";
    fileHint.textContent = "or browse files from your device";
    return;
  }
  if (!file.name.toLowerCase().endsWith(".pdf")) {
    fileInput.value = "";
    fileLabel.textContent = "Drop your PDF here";
    fileHint.textContent = "or browse files from your device";
    message(uploadMessage, "Please choose a PDF file.", "error");
    return;
  }
  selectedFile = file;
  fileLabel.textContent = file.name;
  fileHint.textContent = `${(file.size / (1024 * 1024)).toFixed(2)} MB · Ready to upload`;
}

fileInput.addEventListener("change", () => selectFile(fileInput.files[0]));
for (const eventName of ["dragenter", "dragover"]) {
  dropzone.addEventListener(eventName, (event) => {
    event.preventDefault();
    dropzone.classList.add("dragging");
  });
}
for (const eventName of ["dragleave", "drop"]) {
  dropzone.addEventListener(eventName, (event) => {
    event.preventDefault();
    dropzone.classList.remove("dragging");
  });
}
dropzone.addEventListener("drop", (event) => {
  const files = event.dataTransfer.files;
  if (files.length !== 1) {
    fileInput.value = "";
    selectFile(null);
    message(uploadMessage, "Please drop exactly one PDF file.", "error");
    return;
  }
  selectFile(files[0]);
});

async function responseJSON(response) {
  let data;
  try {
    data = await response.json();
  } catch {
    throw new Error(`The server returned an invalid response (HTTP ${response.status}).`);
  }
  if (!response.ok) {
    throw new Error(typeof data.error === "string" ? data.error : `Request failed (HTTP ${response.status}).`);
  }
  return data;
}

function stopTracking() {
  clearTimeout(timer);
  timer = null;
  requestVersion += 1;
}

function setText(selector, value) {
  document.querySelector(selector).textContent = value;
}

function renderStatus(data) {
  statusEmpty.hidden = true;
  statusResult.hidden = false;
  const status = typeof data.status === "string" ? data.status : "UNKNOWN";
  const badge = document.querySelector("#status-badge");
  badge.textContent = status.replaceAll("_", " ");
  badge.className = `status-badge ${status === "COMPLETED" ? "completed" : status === "FAILED" ? "failed" : ""}`;
  let description = "Processing is in progress. This view refreshes automatically.";
  if (status === "COMPLETED") {
    description = "The configured processing path is complete. This does not guarantee embeddings or search are available.";
  } else if (status === "FAILED") {
    description = "The document could not be processed. See the error below for details.";
  }
  setText("#status-description", description);
  setText("#result-id", data.document_id || trackedId);
  setText("#result-version", data.version == null ? "—" : String(data.version));
  setText("#result-pages", data.page_count == null ? "Not available yet" : String(data.page_count));
  setText("#result-job", [data.job_stage, data.job_status].filter(Boolean).join(" · ") || "Not available");
  const date = data.updated_at ? new Date(data.updated_at) : null;
  setText("#result-updated", date && !Number.isNaN(date.getTime()) ? date.toLocaleString() : "Not available");
  const error = document.querySelector("#result-error");
  error.textContent = data.error || "";
  error.hidden = !data.error;
  pollIndicator.textContent = terminalStatuses.has(status) ? "Auto-refresh stopped" : "Auto-refreshing every 5 seconds";
  if (status === "COMPLETED") loadChunks(trackedId, 0);
  return status;
}

async function showPDF(id, version, page) {
  const requestedWorkspace = workspaceVersion;
  const request = ++pdfRequest;
  try {
    const response = await authenticatedFetch(projectPath(`/documents/${encodeURIComponent(id)}/pdf?version=${encodeURIComponent(version)}`));
    if (!response.ok) await responseJSON(response);
    const blob = await response.blob();
    if (requestedWorkspace !== workspaceVersion || request !== pdfRequest) return;
    if (pdfObjectURL) URL.revokeObjectURL(pdfObjectURL);
    pdfObjectURL = URL.createObjectURL(blob);
    pdfFrame.src = `${pdfObjectURL}#page=${page}`;
    pdfOpen.href = `${pdfObjectURL}#page=${page}`;
    setText("#pdf-heading", `Source PDF · requested page ${page}`);
  } catch (error) {
    if (requestedWorkspace === workspaceVersion && request === pdfRequest) message(chunkMessage, `Could not load PDF: ${error.message}`, "error");
  }
}

function renderChunks(data, id, offset) {
  chunkList.replaceChildren();
  for (const chunk of data.chunks) {
    const row = document.createElement("tr");
    const number = document.createElement("td");
    number.className = "chunk-number";
    number.textContent = String(chunk.index + 1);
    const content = document.createElement("td");
    const preview = document.createElement("div");
    preview.className = "chunk-preview";
    preview.textContent = chunk.text || "No text; this chunk may contain an image.";
    const details = document.createElement("details");
    details.className = "chunk-details";
    details.addEventListener("toggle", () => { preview.hidden = details.open; });
    const summary = document.createElement("summary");
    summary.textContent = chunk.description ? "Full text and description" : "Full text";
    const text = document.createElement("div");
    text.className = "chunk-text";
    text.textContent = chunk.text || "No text; this chunk may contain an image.";
    details.append(summary, text);
    if (chunk.description) {
      const descriptionTitle = document.createElement("h5");
      descriptionTitle.textContent = "Image / table description";
      const description = document.createElement("div");
      description.className = "chunk-text";
      description.textContent = chunk.description;
      details.append(descriptionTitle, description);
    }
    content.append(preview, details);
    const pageCell = document.createElement("td");
    const pages = document.createElement("div");
    pages.className = "chunk-pages";
    for (const page of chunk.pages) {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "page-button";
      button.textContent = String(page);
      button.setAttribute("aria-label", `View PDF page ${page} for chunk ${chunk.index + 1}`);
      button.addEventListener("click", () => showPDF(id, data.version, page));
      pages.append(button);
    }
    if (!chunk.pages.length) pages.textContent = "Unavailable";
    pageCell.append(pages);
    row.append(number, content, pageCell);
    chunkList.append(row);
  }
  chunkPrevious.disabled = offset === 0;
  chunkNext.disabled = !data.has_more;
  setText("#chunks-range", `Showing ${offset + 1}–${offset + data.chunks.length}`);
}

async function loadChunks(id, offset) {
  const request = ++chunkRequest;
  message(chunkMessage, "Loading chunks…");
  chunkPrevious.disabled = true;
  chunkNext.disabled = true;
  try {
    const data = await responseJSON(await authenticatedFetch(projectPath(`/documents/${encodeURIComponent(id)}/chunks?offset=${offset}`), {
      headers: { Accept: "application/json" }, cache: "no-store",
    }));
    if (request !== chunkRequest) return;
    if (!Array.isArray(data.chunks)) throw new Error("The server returned an invalid chunk list");
    if (!data.chunks.length) {
      message(chunkMessage, "No chunks are available for this document.");
      return;
    }
    chunkOffset = offset;
    renderChunks(data, id, offset);
    document.querySelector(".chunk-table-scroll").scrollTop = 0;
    chunkViewer.hidden = false;
    message(chunkMessage, "");
    const page = requestedPDFPage || data.chunks.flatMap((chunk) => chunk.pages)[0] || 1;
    requestedPDFPage = null;
    showPDF(id, data.version, page);
  } catch (error) {
    if (request !== chunkRequest) return;
    message(chunkMessage, `Could not load chunks: ${error.message}. Refresh the document status to retry.`, "error");
    chunkPrevious.disabled = true;
    chunkNext.disabled = true;
  }
}

chunkPrevious.addEventListener("click", () => loadChunks(trackedId, Math.max(0, chunkOffset - chunkPageSize)));
chunkNext.addEventListener("click", () => loadChunks(trackedId, chunkOffset + chunkPageSize));

searchForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  const query = document.querySelector("#search-query").value.trim();
  const documentID = document.querySelector("#search-document").value.trim();
  if (!query) {
    message(searchMessage, "Enter a search query.", "error");
    return;
  }
  const body = { query, top_k: Number(document.querySelector("#search-limit").value) };
  if (documentID) body.document_id = documentID;
  const answering = event.submitter === answerButton;
  const requestedWorkspace = workspaceVersion;
  searchButton.disabled = true;
  answerButton.disabled = true;
  searchResults.replaceChildren();
  answerResult.hidden = true;
  message(searchMessage, answering ? "Retrieving evidence and generating an answer…" : "Searching indexed chunks…");
  try {
    const data = await responseJSON(await authenticatedFetch(projectPath(answering ? "/answer" : "/search"), {
      method: "POST",
      headers: { "Content-Type": "application/json", Accept: "application/json" },
      body: JSON.stringify(body),
    }));
    if (requestedWorkspace !== workspaceVersion) return;
    const results = answering ? data.sources : data.results;
    if (!Array.isArray(results)) throw new Error("The server returned an invalid result list");
    if (answering) {
      if (typeof data.answer !== "string" || typeof data.insufficient_evidence !== "boolean") {
        throw new Error("The server returned an invalid answer");
      }
      answerText.replaceChildren();
      let offset = 0;
      for (const match of data.answer.matchAll(/\[(\d+)\]/g)) {
        answerText.append(document.createTextNode(data.answer.slice(offset, match.index)));
        const citation = document.createElement("a");
        citation.href = `#answer-source-${match[1]}`;
        citation.textContent = match[0];
        answerText.append(citation);
        offset = match.index + match[0].length;
      }
      answerText.append(document.createTextNode(data.answer.slice(offset)));
      document.querySelector("#answer-insufficient").hidden = !data.insufficient_evidence;
      answerResult.hidden = false;
    }
    if (!results.length) {
      message(searchMessage, "No indexed chunks found for this scope.");
      return;
    }
    message(searchMessage, `Found ${results.length} ranked chunk${results.length === 1 ? "" : "s"}.`);
    for (const [index, result] of results.entries()) {
      const card = document.createElement("article");
      card.className = "search-result";
      if (answering) card.id = `answer-source-${result.id}`;
      const head = document.createElement("div");
      head.className = "search-result-head";
      const title = document.createElement("strong");
      title.textContent = `${answering ? `[${result.id}]` : `${index + 1}.`} ${result.filename} · chunk ${result.chunk_index + 1}`;
      const score = document.createElement("span");
      score.textContent = `Hybrid rank score: ${result.score.toFixed(5)}`;
      head.append(title, score);
      const meta = document.createElement("div");
      meta.className = "search-result-meta";
      meta.textContent = `Document ${result.document_id} · version ${result.version} · pages ${result.pages.length ? result.pages.join(", ") : "unavailable"} · vector rank ${result.vector_rank ?? "—"} · keyword rank ${result.keyword_rank ?? "—"}`;
      const text = document.createElement("div");
      text.className = "search-result-text";
      text.textContent = result.text || "This chunk contains no extracted text.";
      card.append(head, meta, text);
      if (result.text_truncated || result.description_truncated) {
        const truncated = document.createElement("div");
        truncated.className = "search-result-meta";
        truncated.textContent = "The evidence sent to the model was truncated.";
        card.append(truncated);
      }
      if (result.description) {
        const details = document.createElement("details");
        const summary = document.createElement("summary");
        summary.textContent = "Image / table description";
        const description = document.createElement("div");
        description.className = "search-result-text";
        description.textContent = result.description;
        details.append(summary, description);
        card.append(details);
      }
      const link = document.createElement("a");
      const page = result.pages[0] || 1;
      link.href = "#chunks";
      link.textContent = result.pages.length ? `View source PDF · page ${page} ↗` : "View source PDF ↗";
      link.addEventListener("click", () => track(result.document_id, page));
      card.append(link);
      searchResults.append(card);
    }
  } catch (error) {
    if (requestedWorkspace === workspaceVersion) message(searchMessage, `${answering ? "Answer generation" : "Search"} failed: ${error.message}`, "error");
  } finally {
    searchButton.disabled = false;
    answerButton.disabled = false;
  }
});

async function fetchStatus(id, version) {
  try {
    const data = await responseJSON(await authenticatedFetch(projectPath(`/documents/${encodeURIComponent(id)}/status`), {
      headers: { Accept: "application/json" },
      cache: "no-store",
    }));
    if (version !== requestVersion) return;
    message(statusMessage, "");
    const status = renderStatus(data);
    if (!terminalStatuses.has(status)) {
      timer = setTimeout(() => fetchStatus(id, version), 5000);
    }
  } catch (error) {
    if (version !== requestVersion) return;
    pollIndicator.textContent = "Auto-refresh paused";
    message(statusMessage, `Could not check status: ${error.message}. Use Check to retry.`, "error");
  }
}

function track(id, page = null) {
  stopTracking();
  chunkRequest += 1;
  pdfRequest += 1;
  requestedPDFPage = page;
  chunkViewer.hidden = true;
  pdfFrame.removeAttribute("src");
  message(chunkMessage, "Chunks will appear when processing is complete.");
  trackedId = id.trim();
  documentIdInput.value = trackedId;
  statusEmpty.hidden = false;
  statusResult.hidden = true;
  message(statusMessage, "Checking document status…");
  fetchStatus(trackedId, requestVersion);
}

statusForm.addEventListener("submit", (event) => {
  event.preventDefault();
  const id = documentIdInput.value.trim();
  if (!id) {
    message(statusMessage, "Enter a document ID.", "error");
    return;
  }
  track(id);
});

refreshButton.addEventListener("click", () => {
  if (trackedId) track(trackedId);
});

uploadForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!selectedFile) {
    message(uploadMessage, "Choose a PDF before starting processing.", "error");
    return;
  }
  const form = new FormData();
  form.append("file", selectedFile);
  const requestedWorkspace = workspaceVersion;
  uploadButton.disabled = true;
  message(uploadMessage, "Uploading document…");
  try {
    const data = await responseJSON(await authenticatedFetch(projectPath("/documents"), { method: "POST", body: form }));
    if (requestedWorkspace !== workspaceVersion) return;
    if (typeof data.document_id !== "string" || !data.document_id) {
      throw new Error("The server did not return a document ID");
    }
    message(uploadMessage, "Upload accepted. Your document is queued for processing.", "success");
    fileInput.value = "";
    selectedFile = null;
    fileLabel.textContent = "Drop your PDF here";
    fileHint.textContent = "or browse files from your device";
    track(data.document_id);
    await loadProjectDocuments();
  } catch (error) {
    if (requestedWorkspace === workspaceVersion) message(uploadMessage, `Upload failed: ${error.message}`, "error");
  } finally {
    uploadButton.disabled = false;
  }
});
