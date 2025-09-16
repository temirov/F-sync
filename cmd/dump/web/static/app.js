(function () {
    "use strict";
    const blobEl = document.getElementById("matrixData");
    if (!blobEl) return;
    const data = JSON.parse(blobEl.textContent || "{}");

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
    };
    const B = {
        followers: indexById(data?.B?.followers || []),
        following: indexById(data?.B?.following || []),
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

    // ----- URL + rendering helpers -----
    function toProfileURL(r) {
        return r.UserName
            ? "https://twitter.com/" + r.UserName
            : "https://twitter.com/i/user/" + r.AccountID;
    }
    function toFollowIntentURL(r) {
        return r.UserName
            ? "https://twitter.com/intent/follow?screen_name=" + encodeURIComponent(r.UserName)
            : "https://twitter.com/intent/user?user_id=" + encodeURIComponent(r.AccountID);
    }
    function escapeHTML(s) {
        return (s || "").replace(/[&<>\"']/g, m => ({
            "&": "&amp;", "<": "&lt;", ">": "&gt;", "\"": "&quot;", "'": "&#39;"
        }[m]));
    }
    function label(r) {
        if (r.DisplayName && r.UserName) return `${escapeHTML(r.DisplayName)} (@${escapeHTML(r.UserName)})`;
        if (r.DisplayName) return escapeHTML(r.DisplayName);
        if (r.UserName) return `@${escapeHTML(r.UserName)}`;
        return escapeHTML(r.AccountID);
    }

    // Render list; when `withFollow` is true, add a Follow intent button
    function renderList(records, withFollow) {
        const host = document.getElementById("cmpOut");
        if (!host) return;
        if (!(records && records.length)) {
            host.innerHTML = '<p class="muted">None</p>';
            return;
        }
        const items = records.map(r => {
            const profile = `<a target="_blank" rel="noopener" href="${escapeHTML(toProfileURL(r))}">${label(r)}</a>`;
            if (!withFollow) return `<li>${profile}</li>`;
            const followURL = escapeHTML(toFollowIntentURL(r));
            return `<li>${profile} — <a class="btn" target="_blank" rel="noopener" href="${followURL}">Follow</a></li>`;
        }).join("");
        host.innerHTML = `<ul class="matrix">${items}</ul>`;
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
        renderList(list, isFollowAction(op));
    }

    document.getElementById("runCmp")?.addEventListener("click", runSelected);
    // Optionally auto-run the selected option on load:
    // runSelected();
})();
