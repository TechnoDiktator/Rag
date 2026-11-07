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
const terminalStatuses = new Set(["COMPLETED", "FAILED"]);
let selectedFile = null;
let timer = null;
let requestVersion = 0;
let trackedId = "";

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
  return status;
}

async function fetchStatus(id, version) {
  try {
    const data = await responseJSON(await fetch(`/api/v1/documents/${encodeURIComponent(id)}/status`, {
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

function track(id) {
  stopTracking();
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
  uploadButton.disabled = true;
  message(uploadMessage, "Uploading document…");
  try {
    const data = await responseJSON(await fetch("/api/v1/documents", { method: "POST", body: form }));
    if (typeof data.document_id !== "string" || !data.document_id) {
      throw new Error("The server did not return a document ID");
    }
    message(uploadMessage, "Upload accepted. Your document is queued for processing.", "success");
    fileInput.value = "";
    selectedFile = null;
    fileLabel.textContent = "Drop your PDF here";
    fileHint.textContent = "or browse files from your device";
    track(data.document_id);
  } catch (error) {
    message(uploadMessage, `Upload failed: ${error.message}`, "error");
  } finally {
    uploadButton.disabled = false;
  }
});
