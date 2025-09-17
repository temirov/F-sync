(function () {
    "use strict";
    const blobEl = document.getElementById("matrixData");
    if (!blobEl) return;
    const data = JSON.parse(blobEl.textContent || "{}");

    const HANDLE_PREFIX = "@";
    const TEXT_UNKNOWN = "Unknown";
    const TEXT_FOLLOW = "Follow";
    const TEXT_MUTED = "Muted";
    const TEXT_BLOCKED = "Blocked";
    const TEXT_NONE = "None";
    const CLASS_ACCOUNT_LIST = "account-list";
    const CLASS_ACCOUNT_CARD = "account-card";
    const CLASS_ACCOUNT_CARD_MAIN = "account-card-main";
    const CLASS_ACCOUNT_CARD_LINK = "account-card-link";
    const CLASS_ACCOUNT_DISPLAY = "account-display";
    const CLASS_ACCOUNT_HANDLE = "account-handle";
    const CLASS_ACCOUNT_META = "account-meta";
    const CLASS_BADGE_MUTED = "badge badge-muted";
    const CLASS_BADGE_BLOCKED = "badge badge-block";
    const CLASS_BUTTON = "btn";
    const CLASS_MUTED_TEXT = "muted";
    const CLASS_SECTION_TOGGLE = "section-toggle";
    const CLASS_SECTION_CONTENT = "section-content";
    const CLASS_HIDDEN = "is-hidden";
    const PROFILE_BASE_URL = "https://twitter.com/";
    const PROFILE_ID_BASE_URL = "https://twitter.com/i/user/";
    const FOLLOW_SCREEN_NAME_URL = "https://twitter.com/intent/follow?screen_name=";
    const FOLLOW_ACCOUNT_ID_URL = "https://twitter.com/intent/user?user_id=";
    const NONE_PLACEHOLDER_HTML = "<p class='" + CLASS_MUTED_TEXT + "'>" + TEXT_NONE + "</p>";
    const ATTRIBUTE_SECTION_TARGET = "data-section-id";
    const ATTRIBUTE_ARIA_CONTROLS = "aria-controls";
    const ATTRIBUTE_ARIA_EXPANDED = "aria-expanded";
    const VALUE_STATE_TRUE = "true";
    const VALUE_STATE_FALSE = "false";
    const TEXT_HIDE = "Hide";
    const TEXT_SHOW = "Show";

    function indexById(arr) {
        const map = new Map();
        for (const rec of (arr || [])) {
            if (rec && rec.AccountID) map.set(rec.AccountID, rec);
        }
        return map;
    }

    // Indexed records for both owners
    const A = {
        followers: indexById(data?.A?.followers || []),
        following: indexById(data?.A?.following || []),
        meta: createMetaLookup(data?.A?.muted || [], data?.A?.blocked || []),
    };
    const B = {
        followers: indexById(data?.B?.followers || []),
        following: indexById(data?.B?.following || []),
        meta: createMetaLookup(data?.B?.muted || [], data?.B?.blocked || []),
    };

    // ----- set helpers -----
    function toList(map) {
        return Array.from(map.values()).sort((a, b) => {
            const ka = (a.DisplayName || a.UserName || a.AccountID || "").toLowerCase();
            const kb = (b.DisplayName || b.UserName || b.AccountID || "").toLowerCase();
            return ka < kb ? -1 : ka > kb ? 1 : 0;
        });
    }
    function difference(a, b) {
        const out = new Map();
        for (const [id, rec] of a) if (!b.has(id)) out.set(id, rec);
        return out;
    }
    function intersection(a, b) {
        const out = new Map();
        for (const [id, rec] of a) if (b.has(id)) out.set(id, rec);
        return out;
    }
    function symmetricDiff(a, b) {
        const out = new Map();
        for (const [id, rec] of a) if (!b.has(id)) out.set(id, rec);
        for (const [id, rec] of b) if (!a.has(id)) out.set(id, rec);
        return out;
    }
    function intersectWithIDSet(recMap, ids) {
        const set = new Set(ids || []);
        const out = new Map();
        for (const [id, rec] of recMap) if (set.has(id)) out.set(id, rec);
        return out;
    }

    function createMetaLookup(mutedIDs, blockedIDs) {
        const mutedSet = new Set(mutedIDs || []);
        const blockedSet = new Set(blockedIDs || []);
        return {
            isMuted(accountID) {
                return mutedSet.has(accountID);
            },
            isBlocked(accountID) {
                return blockedSet.has(accountID);
            },
        };
    }

    // ----- URL + rendering helpers -----
    function toProfileURL(record) {
        return record.UserName
            ? PROFILE_BASE_URL + record.UserName
            : PROFILE_ID_BASE_URL + record.AccountID;
    }
    function toFollowIntentURL(record) {
        return record.UserName
            ? FOLLOW_SCREEN_NAME_URL + encodeURIComponent(record.UserName)
            : FOLLOW_ACCOUNT_ID_URL + encodeURIComponent(record.AccountID);
    }
    function escapeHTML(s) {
        return (s || "").replace(/[&<>\"']/g, m => ({
            "&": "&amp;", "<": "&lt;", ">": "&gt;", "\"": "&quot;", "'": "&#39;"
        }[m]));
    }
    function displayText(record) {
        const display = (record.DisplayName || "").trim();
        if (display) return display;
        const handle = (record.UserName || "").trim();
        if (handle) return HANDLE_PREFIX + handle;
        if (record.AccountID) return record.AccountID;
        return TEXT_UNKNOWN;
    }
    function handleText(record) {
        const handle = (record.UserName || "").trim();
        return handle ? HANDLE_PREFIX + handle : "";
    }
    function computeFlags(accountID, lookups) {
        const sources = lookups || [];
        return {
            muted: sources.some(lookup => lookup && lookup.isMuted(accountID)),
            blocked: sources.some(lookup => lookup && lookup.isBlocked(accountID)),
        };
    }

    // Render list; when `withFollow` is true, add a Follow intent button
    function renderList(records, withFollow, metaLookups) {
        const host = document.getElementById("cmpOut");
        if (!host) return;
        if (!(records && records.length)) {
            host.innerHTML = NONE_PLACEHOLDER_HTML;
            return;
        }
        const items = records.map(record => {
            const profileURL = escapeHTML(toProfileURL(record));
            const displayHTML = `<strong class="${CLASS_ACCOUNT_DISPLAY}">${escapeHTML(displayText(record))}</strong>`;
            const linkHTML = `<a class="${CLASS_ACCOUNT_CARD_LINK}" target="_blank" rel="noopener" href="${profileURL}">${displayHTML}</a>`;
            const handleValue = handleText(record);
            const handleHTML = handleValue ? `<span class="${CLASS_ACCOUNT_HANDLE}">${escapeHTML(handleValue)}</span>` : "";
            const flags = computeFlags(record.AccountID, metaLookups);
            const metaPieces = [];
            if (flags.muted) {
                metaPieces.push(`<span class="${CLASS_BADGE_MUTED}">${TEXT_MUTED}</span>`);
            }
            if (flags.blocked) {
                metaPieces.push(`<span class="${CLASS_BADGE_BLOCKED}">${TEXT_BLOCKED}</span>`);
            }
            if (withFollow) {
                const followURL = escapeHTML(toFollowIntentURL(record));
                metaPieces.push(`<a class="${CLASS_BUTTON}" target="_blank" rel="noopener" href="${followURL}">${TEXT_FOLLOW}</a>`);
            }
            const metaHTML = metaPieces.length ? `<div class="${CLASS_ACCOUNT_META}">${metaPieces.join("")}</div>` : "";
            return `<li class="${CLASS_ACCOUNT_CARD}"><div class="${CLASS_ACCOUNT_CARD_MAIN}">${linkHTML}${handleHTML}</div>${metaHTML}</li>`;
        }).join("");
        host.innerHTML = `<ul class="${CLASS_ACCOUNT_LIST}">${items}</ul>`;
    }

    // Decide which operations should show actionable Follow buttons
    function isFollowAction(op) {
        switch (op) {
            case "B_following_minus_A_following": // A can follow B's unique following
            case "A_following_minus_B_following": // B can follow A's unique following
            case "A_followers_minus_following":   // A can follow their own unfollowed followers
            case "B_followers_minus_following":   // B can follow their own unfollowed followers
                return true;
            default:
                return false; // mutual / blocked / symmetric diff → info-only
        }
    }

    function metaSourcesForOperation(op) {
        switch (op) {
            case "B_following_minus_A_following":
            case "B_followers_minus_following":
            case "B_blocked_intersect_following":
                return [B.meta];
            case "A_following_minus_B_following":
            case "A_followers_minus_following":
            case "A_blocked_intersect_following":
                return [A.meta];
            case "mutual_following":
            case "symdiff_following":
                return [A.meta, B.meta];
            default:
                return [A.meta, B.meta];
        }
    }

    function runSelected() {
        const op = document.getElementById("cmpOp").value;
        let result;
        switch (op) {
            case "B_following_minus_A_following":
                result = difference(B.following, A.following);
                break;
            case "A_following_minus_B_following":
                result = difference(A.following, B.following);
                break;
            case "mutual_following":
                result = intersection(A.following, B.following);
                break;
            case "A_followers_minus_following":
                result = difference(A.followers, A.following);
                break;
            case "B_followers_minus_following":
                result = difference(B.followers, B.following);
                break;
            case "A_blocked_intersect_following":
                result = intersectWithIDSet(A.following, data?.A?.blocked || []);
                break;
            case "B_blocked_intersect_following":
                result = intersectWithIDSet(B.following, data?.B?.blocked || []);
                break;
            case "symdiff_following":
                result = symmetricDiff(A.following, B.following);
                break;
            default:
                result = new Map();
        }
        const list = toList(result);
        renderList(list, isFollowAction(op), metaSourcesForOperation(op));
    }

    document.getElementById("runCmp")?.addEventListener("click", runSelected);
    attachSectionToggleBehavior();
    // Optionally auto-run the selected option on load:
    // runSelected();

    function attachSectionToggleBehavior() {
        const toggleButtons = document.querySelectorAll("." + CLASS_SECTION_TOGGLE);
        toggleButtons.forEach(toggleButton => {
            if (!(toggleButton instanceof HTMLElement)) return;
            const sectionIdentifier = toggleButton.getAttribute(ATTRIBUTE_SECTION_TARGET) || "";
            if (!sectionIdentifier) return;
            const targetElement = document.getElementById(sectionIdentifier);
            if (!(targetElement instanceof HTMLElement)) return;
            if (!targetElement.classList.contains(CLASS_SECTION_CONTENT)) return;
            toggleButton.addEventListener("click", event => {
                event.preventDefault();
                toggleSectionVisibility(toggleButton, targetElement);
            });
            const shouldStartHidden = targetElement.classList.contains(CLASS_HIDDEN);
            updateSectionVisibility(toggleButton, targetElement, shouldStartHidden);
            if (!toggleButton.getAttribute(ATTRIBUTE_ARIA_CONTROLS)) {
                toggleButton.setAttribute(ATTRIBUTE_ARIA_CONTROLS, sectionIdentifier);
            }
        });
    }

    function toggleSectionVisibility(toggleButton, targetElement) {
        const isCurrentlyHidden = targetElement.classList.contains(CLASS_HIDDEN);
        updateSectionVisibility(toggleButton, targetElement, !isCurrentlyHidden);
    }

    function updateSectionVisibility(toggleButton, targetElement, shouldHide) {
        if (shouldHide) {
            targetElement.classList.add(CLASS_HIDDEN);
            toggleButton.setAttribute(ATTRIBUTE_ARIA_EXPANDED, VALUE_STATE_FALSE);
            toggleButton.textContent = TEXT_SHOW;
        } else {
            targetElement.classList.remove(CLASS_HIDDEN);
            toggleButton.setAttribute(ATTRIBUTE_ARIA_EXPANDED, VALUE_STATE_TRUE);
            toggleButton.textContent = TEXT_HIDE;
        }
    }
})();
