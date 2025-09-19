(function () {
    "use strict";

    const ID_MATRIX_DATA = "matrixData";
    const ID_ARCHIVE_INPUT = "archiveInput";
    const ID_ARCHIVE_DROPZONE = "archiveDropzone";
    const ID_BROWSE_BUTTON = "browseArchivesButton";
    const ID_UPLOADS_LIST = "uploadsList";
    const ID_UPLOADS_PLACEHOLDER = "uploadsPlaceholder";
    const ID_UPLOAD_ALERTS = "uploadAlerts";
    const ID_COMPARE_BUTTON = "compareButton";
    const ID_RESET_BUTTON = "resetUploadsButton";
    const ID_COMPARISON_PANEL = "comparisonPanel";
    const ID_COMPARISON_OPERATION = "cmpOp";
    const ID_COMPARISON_OUTPUT = "cmpOut";
    const ID_COMPARISON_BUTTON = "runCmp";

    const ROUTE_UPLOADS = "/api/uploads";
    const HTTP_METHOD_POST = "POST";
    const HTTP_METHOD_DELETE = "DELETE";
    const JSON_KEY_UPLOADS = "uploads";
    const JSON_KEY_ERROR = "error";
    const JSON_KEY_COMPARISON_READY = "comparisonReady";

    const CLASS_DROPZONE_ACTIVE = "is-dragover";
    const CLASS_SECTION_TOGGLE = "section-toggle";
    const CLASS_SECTION_CONTENT = "section-content";
    const CLASS_HIDDEN = "is-hidden";

    const ATTRIBUTE_SECTION_TARGET = "data-section-id";
    const ATTRIBUTE_ARIA_CONTROLS = "aria-controls";
    const ATTRIBUTE_ARIA_EXPANDED = "aria-expanded";

    const VALUE_TRUE = "true";
    const VALUE_FALSE = "false";

    const TEXT_UPLOAD_GENERIC_ERROR = "Upload failed. Please verify the file format.";
    const TEXT_RESET_GENERIC_ERROR = "Reset failed. Please try again.";
    const TEXT_UPLOAD_PLACEHOLDER = "No archives uploaded yet.";
    const TEXT_UNKNOWN = "Unknown";
    const TEXT_HANDLE_PREFIX = "@";
    const TEXT_FOLLOW_BUTTON = "Follow";
    const TEXT_MUTED = "Muted";
    const TEXT_BLOCKED = "Blocked";
    const TEXT_NONE = "None";
    const TEXT_HIDE = "Hide";
    const TEXT_SHOW = "Show";

    const PROFILE_BASE_URL = "https://twitter.com/";
    const FOLLOW_SCREEN_NAME_URL = "https://twitter.com/intent/follow?screen_name=";

    initializeUploadUI();
    initializeMatrixFeatures();

    function initializeUploadUI() {
        const fileInputElement = document.getElementById(ID_ARCHIVE_INPUT);
        const dropzoneElement = document.getElementById(ID_ARCHIVE_DROPZONE);
        const browseButtonElement = document.getElementById(ID_BROWSE_BUTTON);
        const compareButtonElement = document.getElementById(ID_COMPARE_BUTTON);
        const comparisonPanelElement = document.getElementById(ID_COMPARISON_PANEL);
        const uploadsListElement = document.getElementById(ID_UPLOADS_LIST);
        const placeholderElement = document.getElementById(ID_UPLOADS_PLACEHOLDER);
        const alertContainerElement = document.getElementById(ID_UPLOAD_ALERTS);
        const resetButtonElement = document.getElementById(ID_RESET_BUTTON);

        if (!fileInputElement || !dropzoneElement || !browseButtonElement || !compareButtonElement || !comparisonPanelElement || !uploadsListElement || !alertContainerElement) {
            return;
        }

        const hasComparison = comparisonPanelElement.dataset.hasComparison === VALUE_TRUE;
        updateCompareButton(compareButtonElement, hasComparison);

        browseButtonElement.addEventListener("click", () => fileInputElement.click());

        fileInputElement.addEventListener("change", () => {
            const { files } = fileInputElement;
            if (files && files.length > 0) {
                uploadArchives(files, {
                    compareButtonElement,
                    uploadsListElement,
                    placeholderElement,
                    alertContainerElement,
                });
                fileInputElement.value = "";
            }
        });

        dropzoneElement.addEventListener("dragover", event => {
            event.preventDefault();
            dropzoneElement.classList.add(CLASS_DROPZONE_ACTIVE);
        });

        dropzoneElement.addEventListener("dragleave", () => {
            dropzoneElement.classList.remove(CLASS_DROPZONE_ACTIVE);
        });

        dropzoneElement.addEventListener("drop", event => {
            event.preventDefault();
            dropzoneElement.classList.remove(CLASS_DROPZONE_ACTIVE);
            const items = event.dataTransfer?.files;
            if (items && items.length > 0) {
                uploadArchives(items, {
                    compareButtonElement,
                    uploadsListElement,
                    placeholderElement,
                    alertContainerElement,
                });
            }
        });

        compareButtonElement.addEventListener("click", () => {
            window.location.reload();
        });

        if (resetButtonElement) {
            resetButtonElement.addEventListener("click", () => {
                clearUploads({
                    compareButtonElement,
                    uploadsListElement,
                    placeholderElement,
                    alertContainerElement,
                });
            });
        }
    }

    function uploadArchives(fileList, options) {
        const formData = new FormData();
        for (const file of fileList) {
            if (file instanceof File) {
                formData.append("archives", file);
            }
        }
        if (!formData.has("archives")) {
            return;
        }

        setAlertMessage(options.alertContainerElement, "", false);

        fetch(ROUTE_UPLOADS, {
            method: HTTP_METHOD_POST,
            body: formData,
        }).then(response => {
            if (!response.ok) {
                return response.json().catch(() => ({})).then(body => {
                    throw new Error(body[JSON_KEY_ERROR] || TEXT_UPLOAD_GENERIC_ERROR);
                });
            }
            return response.json();
        }).then(body => {
            const uploads = Array.isArray(body[JSON_KEY_UPLOADS]) ? body[JSON_KEY_UPLOADS] : [];
            renderUploadsList(uploads, options.uploadsListElement, options.placeholderElement);
            const comparisonReady = Boolean(body[JSON_KEY_COMPARISON_READY]);
            updateCompareButton(options.compareButtonElement, comparisonReady);
        }).catch(error => {
            setAlertMessage(options.alertContainerElement, error.message || TEXT_UPLOAD_GENERIC_ERROR, true);
        });
    }

    function clearUploads(options) {
        fetch(ROUTE_UPLOADS, {
            method: HTTP_METHOD_DELETE,
        }).then(response => {
            if (!response.ok) {
                return response.json().catch(() => ({})).then(body => {
                    throw new Error(body[JSON_KEY_ERROR] || TEXT_RESET_GENERIC_ERROR);
                });
            }
            renderUploadsList([], options.uploadsListElement, options.placeholderElement);
            updateCompareButton(options.compareButtonElement, false);
            setAlertMessage(options.alertContainerElement, "", false);
        }).catch(error => {
            setAlertMessage(options.alertContainerElement, error.message || TEXT_RESET_GENERIC_ERROR, true);
        });
    }

    function renderUploadsList(uploads, listElement, placeholderElement) {
        if (!listElement) {
            return;
        }
        listElement.innerHTML = "";
        if (!uploads || uploads.length === 0) {
            const placeholder = placeholderElement || document.createElement("li");
            placeholder.textContent = TEXT_UPLOAD_PLACEHOLDER;
            placeholder.className = "list-group-item text-muted";
            placeholder.id = ID_UPLOADS_PLACEHOLDER;
            listElement.appendChild(placeholder);
            return;
        }
        uploads.forEach(upload => {
            const item = document.createElement("li");
            item.className = "list-group-item";
            const wrapper = document.createElement("div");
            wrapper.className = "d-flex flex-column";

            const slotBadge = document.createElement("span");
            slotBadge.className = "badge bg-info text-dark align-self-start mb-2";
            slotBadge.textContent = upload.slotLabel || "Archive";

            const ownerLine = document.createElement("span");
            ownerLine.className = "fw-semibold";
            ownerLine.textContent = upload.ownerLabel || TEXT_UNKNOWN;

            const fileLine = document.createElement("span");
            fileLine.className = "text-muted small";
            fileLine.textContent = upload.fileName || "";

            wrapper.appendChild(slotBadge);
            wrapper.appendChild(ownerLine);
            wrapper.appendChild(fileLine);
            item.appendChild(wrapper);
            listElement.appendChild(item);
        });
    }

    function updateCompareButton(compareButtonElement, enabled) {
        if (!compareButtonElement) {
            return;
        }
        if (enabled) {
            compareButtonElement.removeAttribute("disabled");
            compareButtonElement.classList.remove("btn-secondary");
            compareButtonElement.classList.add("btn-success");
        } else {
            compareButtonElement.setAttribute("disabled", VALUE_TRUE);
            compareButtonElement.classList.remove("btn-success");
            compareButtonElement.classList.add("btn-secondary");
        }
    }

    function setAlertMessage(containerElement, message, isError) {
        if (!containerElement) {
            return;
        }
        containerElement.innerHTML = "";
        if (!message) {
            return;
        }
        const alert = document.createElement("div");
        alert.className = `alert ${isError ? "alert-danger" : "alert-info"}`;
        alert.textContent = message;
        containerElement.appendChild(alert);
    }

    function initializeMatrixFeatures() {
        setupSectionToggles();

        const matrixElement = document.getElementById(ID_MATRIX_DATA);
        if (!matrixElement) {
            return;
        }
        const matrixJSON = matrixElement.textContent || "";
        if (!matrixJSON.trim()) {
            return;
        }
        let matrixData;
        try {
            matrixData = JSON.parse(matrixJSON);
        } catch (error) {
            return;
        }
        if (!matrixData || !matrixData.A || !matrixData.B) {
            return;
        }
        initializeComparisonCalculator(matrixData);
    }

    function setupSectionToggles() {
        const toggleButtons = document.querySelectorAll(`.${CLASS_SECTION_TOGGLE}`);
        toggleButtons.forEach(button => {
            const targetId = button.getAttribute(ATTRIBUTE_SECTION_TARGET);
            const target = targetId ? document.getElementById(targetId) : null;
            const controlsId = button.getAttribute(ATTRIBUTE_ARIA_CONTROLS);
            if (target && controlsId) {
                button.addEventListener("click", () => {
                    const isExpanded = button.getAttribute(ATTRIBUTE_ARIA_EXPANDED) === VALUE_TRUE;
                    const nextState = !isExpanded;
                    button.setAttribute(ATTRIBUTE_ARIA_EXPANDED, nextState ? VALUE_TRUE : VALUE_FALSE);
                    button.textContent = nextState ? TEXT_HIDE : TEXT_SHOW;
                    target.classList.toggle(CLASS_HIDDEN, !nextState);
                });
            }
        });
    }

    function initializeComparisonCalculator(data) {
        const ownerAData = buildOwnerData(data.A);
        const ownerBData = buildOwnerData(data.B);
        const metaContext = { A: ownerAData, B: ownerBData };
        const operationSelect = document.getElementById(ID_COMPARISON_OPERATION);
        const runButton = document.getElementById(ID_COMPARISON_BUTTON);
        const outputContainer = document.getElementById(ID_COMPARISON_OUTPUT);

        if (!operationSelect || !runButton || !outputContainer) {
            return;
        }

        runButton.addEventListener("click", () => {
            const operation = operationSelect.value;
            const results = computeComparison(operation, ownerAData, ownerBData);
            renderComparisonResults(results, outputContainer, operation, metaContext);
        });
    }

    function buildOwnerData(owner) {
        return {
            followers: indexById(owner?.followers || []),
            following: indexById(owner?.following || []),
            muted: new Set(owner?.muted || []),
            blocked: new Set(owner?.blocked || []),
        };
    }

    function indexById(records) {
        const indexed = new Map();
        (records || []).forEach(record => {
            if (record && record.AccountID) {
                indexed.set(record.AccountID, record);
            }
        });
        return indexed;
    }

    function computeComparison(operation, ownerAData, ownerBData) {
        switch (operation) {
            case "B_following_minus_A_following":
                return difference(ownerBData.following, ownerAData.following);
            case "A_following_minus_B_following":
                return difference(ownerAData.following, ownerBData.following);
            case "mutual_following":
                return intersection(ownerAData.following, ownerBData.following);
            case "A_followers_minus_following":
                return difference(ownerAData.followers, ownerAData.following);
            case "B_followers_minus_following":
                return difference(ownerBData.followers, ownerBData.following);
            case "A_blocked_intersect_following":
                return intersectWithIDs(ownerAData.following, ownerAData.blocked);
            case "B_blocked_intersect_following":
                return intersectWithIDs(ownerBData.following, ownerBData.blocked);
            case "symdiff_following":
                return symmetricDifference(ownerAData.following, ownerBData.following);
            default:
                return new Map();
        }
    }

    function difference(first, second) {
        const results = new Map();
        first.forEach((record, accountId) => {
            if (!second.has(accountId)) {
                results.set(accountId, record);
            }
        });
        return results;
    }

    function intersection(first, second) {
        const results = new Map();
        first.forEach((record, accountId) => {
            if (second.has(accountId)) {
                results.set(accountId, record);
            }
        });
        return results;
    }

    function symmetricDifference(first, second) {
        const results = new Map();
        first.forEach((record, accountId) => {
            if (!second.has(accountId)) {
                results.set(accountId, record);
            }
        });
        second.forEach((record, accountId) => {
            if (!first.has(accountId)) {
                results.set(accountId, record);
            }
        });
        return results;
    }

    function intersectWithIDs(records, identifiers) {
        const results = new Map();
        records.forEach((record, accountId) => {
            if (identifiers.has(accountId)) {
                results.set(accountId, record);
            }
        });
        return results;
    }

    function renderComparisonResults(resultsMap, container, operation, metaContext) {
        if (!container) {
            return;
        }
        const records = Array.from(resultsMap.values());
        records.sort((first, second) => {
            const firstKey = (first.DisplayName || first.UserName || first.AccountID || "").toLowerCase();
            const secondKey = (second.DisplayName || second.UserName || second.AccountID || "").toLowerCase();
            return firstKey.localeCompare(secondKey);
        });
        if (records.length === 0) {
            container.innerHTML = `<p class="text-muted fst-italic">${TEXT_NONE}</p>`;
            return;
        }
        const metaSources = metaSourcesForOperation(operation, metaContext);
        const itemsHTML = records.map(record => renderAccountRecord(record, metaSources, isFollowAction(operation))).join("");
        container.innerHTML = `<ul class="list-unstyled mb-0">${itemsHTML}</ul>`;
    }

    function metaSourcesForOperation(operation, metaContext) {
        switch (operation) {
            case "B_following_minus_A_following":
            case "B_followers_minus_following":
            case "B_blocked_intersect_following":
                return [metaLookupForOwner(metaContext.B)];
            case "A_following_minus_B_following":
            case "A_followers_minus_following":
            case "A_blocked_intersect_following":
                return [metaLookupForOwner(metaContext.A)];
            case "mutual_following":
            case "symdiff_following":
                return [metaLookupForOwner(metaContext.A), metaLookupForOwner(metaContext.B)];
            default:
                return [metaLookupForOwner(metaContext.A), metaLookupForOwner(metaContext.B)];
        }
    }

    function metaLookupForOwner(ownerData) {
        const mutedSet = ownerData?.muted instanceof Set ? ownerData.muted : new Set();
        const blockedSet = ownerData?.blocked instanceof Set ? ownerData.blocked : new Set();
        return {
            isMuted(accountId) {
                return mutedSet.has(accountId);
            },
            isBlocked(accountId) {
                return blockedSet.has(accountId);
            },
        };
    }

    function renderAccountRecord(record, metaSources, includeFollowAction) {
        const trimmedDisplayName = (record.DisplayName || "").trim();
        const trimmedHandle = (record.UserName || "").trim();
        const hasDisplayName = trimmedDisplayName !== "";
        const hasHandle = trimmedHandle !== "";
        const formattedHandle = hasHandle ? `${TEXT_HANDLE_PREFIX}${trimmedHandle}` : "";
        let displayText = TEXT_UNKNOWN;
        if (hasDisplayName && hasHandle) {
            displayText = `${trimmedDisplayName} (${formattedHandle})`;
        } else if (hasDisplayName) {
            displayText = trimmedDisplayName;
        } else if (hasHandle) {
            displayText = formattedHandle;
        }
        const profileURL = hasHandle ? `${PROFILE_BASE_URL}${trimmedHandle}` : "";
        const badges = [];
        if (metaSources.some(source => source.isMuted(record.AccountID))) {
            badges.push(`<span class="badge text-bg-warning me-2">${TEXT_MUTED}</span>`);
        }
        if (metaSources.some(source => source.isBlocked(record.AccountID))) {
            badges.push(`<span class="badge text-bg-danger">${TEXT_BLOCKED}</span>`);
        }
        if (includeFollowAction && hasHandle) {
            const intentURL = `${FOLLOW_SCREEN_NAME_URL}${encodeURIComponent(trimmedHandle)}`;
            badges.push(`<a class="btn btn-sm btn-outline-primary ms-2" target="_blank" rel="noopener" href="${intentURL}">${TEXT_FOLLOW_BUTTON}</a>`);
        }
        const badgeHTML = badges.length ? `<div class="mt-2">${badges.join(" ")}</div>` : "";
        const handleHTML = hasHandle && hasDisplayName ? `<span class="text-muted small">${escapeHTML(formattedHandle)}</span>` : "";
        const strongNameHTML = `<strong class="d-block">${escapeHTML(displayText)}</strong>`;
        const nameHTML = profileURL
            ? `<a class="text-decoration-none" target="_blank" rel="noopener" href="${profileURL}">${strongNameHTML}</a>`
            : strongNameHTML;
        return `<li class="mb-3 pb-3 border-bottom">${nameHTML}${handleHTML}${badgeHTML}</li>`;
    }

    function escapeHTML(input) {
        return (input || "").replace(/[&<>\"']/g, match => ({
            "&": "&amp;",
            "<": "&lt;",
            ">": "&gt;",
            "\"": "&quot;",
            "'": "&#39;",
        })[match]);
    }

    function isFollowAction(operation) {
        switch (operation) {
            case "B_following_minus_A_following":
            case "A_following_minus_B_following":
            case "A_followers_minus_following":
            case "B_followers_minus_following":
                return true;
            default:
                return false;
        }
    }
})();
