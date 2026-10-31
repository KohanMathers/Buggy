(function () {
  "use strict";
  const $ = (s, el = document) => el.querySelector(s);
  const $$ = (s, el = document) => Array.from(el.querySelectorAll(s));
  const csrf = () => $('meta[name="csrf-token"]')?.content || "";
  const debounce = (fn, ms) => { let t; return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); }; };
  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

  async function api(method, url, body) {
    const opts = { method, headers: { Accept: "application/json", "X-CSRF-Token": csrf(), "X-Requested-With": "fetch" } };
    if (body instanceof FormData) opts.body = body;
    else if (body !== undefined) { opts.headers["Content-Type"] = "application/json"; opts.body = JSON.stringify(body); }
    const r = await fetch(url, opts);
    const data = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(data.error || r.statusText);
    return data;
  }

  function toast(msg) {
    const t = document.createElement("div");
    t.className = "toast"; t.textContent = msg;
    document.body.appendChild(t);
    setTimeout(() => t.remove(), 2600);
  }

  $$("[data-theme-toggle]").forEach((b) => b.addEventListener("click", () => {
    const root = document.documentElement;
    const next = { auto: "dark", dark: "light", light: "auto" }[root.dataset.theme] || "auto";
    root.dataset.theme = next; localStorage.setItem("buggy-theme", next); toast("Theme: " + next);
  }));

  document.addEventListener("click", (e) => {
    $$("details.menu[open], details.popover[open]").forEach((d) => { if (!d.contains(e.target)) d.removeAttribute("open"); });
  });

  document.addEventListener("submit", (e) => {
    const msg = e.target.dataset.confirm;
    if (msg && !confirm(msg)) e.preventDefault();
  }, true);

  $$("[data-open]").forEach((b) => b.addEventListener("click", (e) => {
    e.preventDefault(); e.stopPropagation();
    b.closest("details")?.removeAttribute("open");
    const target = $(b.dataset.open);
    if (!target) return;
    target.open = true;
    target.scrollIntoView({ block: "nearest" });
    setTimeout(() => $("input:not([type=hidden]), select, textarea", target)?.focus(), 30);
  }));

  $$("[data-tabs]").forEach((tabs) => {
    const timeline = tabs.parentElement.querySelector("[data-timeline]");
    if (!timeline) return;
    const apply = (filter) => {
      let shown = 0;
      $$("[data-kind]", timeline).forEach((el) => {
        const ok = filter === "all" || el.dataset.kind === filter;
        el.classList.toggle("hidden", !ok);
        if (ok) shown++;
      });
      $("[data-empty]", timeline)?.classList.toggle("hidden", shown > 0);
      $$("button", tabs).forEach((b) => b.classList.toggle("active", b.dataset.filter === filter));
      try { localStorage.setItem("buggy-activity-tab", filter); } catch { }
    };
    $$("button", tabs).forEach((b) => b.addEventListener("click", () => apply(b.dataset.filter)));
    let initial = "all";
    try { initial = localStorage.getItem("buggy-activity-tab") || initial; } catch { }
    if (location.hash.startsWith("#comment-")) initial = "comment";
    if (!$(`button[data-filter="${initial}"]`, tabs)) initial = "all";
    apply(initial);
  });

  const codesText = () => $$("#recovery-codes > div").map((d) => d.textContent.trim()).join("\n");
  $("[data-copy-codes]")?.addEventListener("click", async () => {
    try { await navigator.clipboard.writeText(codesText()); toast("Recovery codes copied"); }
    catch { toast("Copy failed, select the codes and copy them manually"); }
  });
  $("[data-download-codes]")?.addEventListener("click", (e) => {
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([codesText() + "\n"], { type: "text/plain" }));
    a.download = e.currentTarget.dataset.filename || "recovery-codes.txt";
    a.click();
    URL.revokeObjectURL(a.href);
  });

  const modAction = $("#mod-action");
  if (modAction) {
    const form = modAction.form;
    const sync = () => {
      const a = modAction.value;
      $("[data-mod-duration]", form).classList.toggle("hidden", !(a === "lock" || a === "mute"));
      $("[data-mod-reason]", form).classList.toggle("hidden", !["lock", "ban", "mute", "kick", "shadowban"].includes(a));
      const forever = $("option[data-mute-only]", form);
      forever.hidden = a !== "mute";
      if (a !== "mute" && forever.selected) $('[name="duration"]', form).value = "7d";
    };
    modAction.addEventListener("change", sync);
    sync();
    form.addEventListener("submit", (e) => {
      const a = modAction.value;
      if (["ban", "lock", "kick", "shadowban", "revoke_tokens"].includes(a) && !confirm(`Apply "${modAction.selectedOptions[0].textContent.split(":")[0]}" to this account?`)) e.preventDefault();
    });
  }

  $$("[data-autosubmit]").forEach((el) => el.addEventListener("change", () => el.form.requestSubmit()));

  $$("form[data-toggle]").forEach((form) => form.addEventListener("submit", async (e) => {
    e.preventDefault();
    const btn = form.querySelector("button");
    const on = form.querySelector('input[name="on"]');
    try {
      const data = await api("POST", form.action, new FormData(form));
      const nowOn = on.value === "1";
      on.value = nowOn ? "0" : "1";
      btn.classList.toggle("on", nowOn);
      const kind = form.dataset.toggle;
      const count = form.querySelector("[data-count]");
      if (count) count.textContent = kind === "vote" ? data.votes : data.watchers;
      const label = form.querySelector("[data-label]");
      if (label) label.textContent = nowOn ? label.dataset.on : label.dataset.off;
    } catch (err) { toast(err.message); }
  }));

  $$("[data-editor]").forEach((ed) => {
    const ta = $("textarea", ed), pv = $(".preview", ed);
    $$(".editor-tabs button", ed).forEach((b) => b.addEventListener("click", async () => {
      $$(".editor-tabs button", ed).forEach((x) => x.classList.toggle("active", x === b));
      const preview = b.dataset.tab === "preview";
      ta.classList.toggle("hidden", preview); pv.classList.toggle("hidden", !preview);
      if (preview) {
        pv.innerHTML = '<span class="muted">Rendering…</span>';
        try { pv.innerHTML = (await api("POST", "/api/v1/preview", { text: ta.value })).html || '<span class="muted">Nothing to preview</span>'; }
        catch (err) { pv.textContent = err.message; }
      }
    }));
    ta.addEventListener("keydown", (e) => { if ((e.ctrlKey || e.metaKey) && e.key === "Enter") ta.form?.requestSubmit(); });
  });

  let pop = null;
  function closePop() { pop?.remove(); pop = null; }
  $$("textarea[data-mentions]").forEach((ta) => {
    const lookup = debounce(async () => {
      const upto = ta.value.slice(0, ta.selectionStart);
      const m = upto.match(/(^|\s)@([\w.-]{1,30})$/);
      if (!m) return closePop();
      let users = [];
      try { users = await api("GET", "/api/v1/users?prefix=" + encodeURIComponent(m[2])); } catch { return closePop(); }
      if (!users.length) return closePop();
      closePop();
      pop = document.createElement("div"); pop.className = "mention-pop";
      users.forEach((u, i) => {
        const b = document.createElement("button"); b.type = "button"; if (!i) b.classList.add("active");
        b.innerHTML = `<b>@${esc(u.username)}</b> <span class="muted small">${esc(u.name)}</span>`;
        b.addEventListener("mousedown", (e) => { e.preventDefault(); insert(u.username); });
        pop.appendChild(b);
      });
      const r = ta.getBoundingClientRect();
      pop.style.left = r.left + window.scrollX + 12 + "px"; pop.style.top = r.bottom + window.scrollY - 8 + "px";
      document.body.appendChild(pop);
      function insert(name) {
        const pos = ta.selectionStart, before = ta.value.slice(0, pos).replace(/@[\w.-]*$/, "@" + name + " ");
        ta.value = before + ta.value.slice(pos); ta.selectionStart = ta.selectionEnd = before.length; ta.focus(); closePop();
      }
      pop.insert = insert;
    }, 180);
    ta.addEventListener("input", lookup);
    ta.addEventListener("blur", () => setTimeout(closePop, 150));
    ta.addEventListener("keydown", (e) => {
      if (!pop) return;
      const items = $$("button", pop), i = items.findIndex((b) => b.classList.contains("active"));
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault(); items[i]?.classList.remove("active");
        items[(i + (e.key === "ArrowDown" ? 1 : items.length - 1)) % items.length].classList.add("active");
      } else if (e.key === "Enter" || e.key === "Tab") {
        e.preventDefault(); pop.insert(items[i].querySelector("b").textContent.slice(1));
      } else if (e.key === "Escape") closePop();
    });
  });

  $$(".dropzone").forEach((dz) => {
    const input = $('input[type="file"]', dz);
    const list = $(".files", dz);
    const show = () => { if (list) list.textContent = Array.from(input.files).map((f) => f.name).join(", "); };
    ["dragenter", "dragover"].forEach((ev) => dz.addEventListener(ev, (e) => { e.preventDefault(); dz.classList.add("drag"); }));
    ["dragleave", "drop"].forEach((ev) => dz.addEventListener(ev, (e) => { e.preventDefault(); dz.classList.remove("drag"); }));
    dz.addEventListener("drop", (e) => { input.files = e.dataTransfer.files; input.dispatchEvent(new Event("change")); });
    input.addEventListener("change", show);
  });

  const wizard = $("form[data-wizard]");
  if (wizard) {
    const project = wizard.dataset.project;
    const steps = $$(".wizard-step", wizard);
    const markers = $$(".steps .s");
    let cur = 0;

    const values = (name) => {
      const els = $$(`[name="${name}"], [name="f_${name}"]`, wizard);
      return els.filter((el) => (el.type === "checkbox" || el.type === "radio") ? el.checked : true)
        .flatMap((el) => el.tagName === "SELECT" && el.multiple ? Array.from(el.selectedOptions).map((o) => o.value) : [el.value])
        .filter((v) => v !== "");
    };
    const applyConditions = () => {
      $$("[data-show-if]", wizard).forEach((wrap) => {
        const [field, want] = wrap.dataset.showIf.split("=");
        const vals = values(field), wants = want.split("|");
        const ok = wants.includes("*") ? vals.some((v) => v.trim()) : vals.some((v) => wants.includes(v));
        wrap.classList.toggle("hidden", !ok);
      });
    };
    const validate = (stepEl) => {
      let ok = true;
      $$(".field", stepEl).forEach((f) => {
        if (f.closest(".hidden")) return;
        const req = f.querySelector("[data-required]");
        f.classList.remove("has-error");
        $(".error.js", f)?.remove();
        if (!req) return;
        const name = req.name || req.id;
        const filled = req.classList.contains("options") ? $$("input:checked", req).length > 0 : (req.value || "").trim() !== "";
        if (!filled) {
          ok = false; f.classList.add("has-error");
          const e = document.createElement("div"); e.className = "error js"; e.textContent = "This is required"; f.appendChild(e);
        }
      });
      return ok;
    };
    const show = (i) => {
      cur = Math.max(0, Math.min(i, steps.length - 1));
      steps.forEach((s, j) => s.classList.toggle("hidden", j !== cur));
      markers.forEach((m, j) => { m.classList.toggle("active", j === cur); m.classList.toggle("done", j < cur); });
      $("[data-prev]", wizard)?.classList.toggle("hidden", cur === 0);
      $("[data-next]", wizard)?.classList.toggle("hidden", cur === steps.length - 1);
      $("[data-submit]", wizard)?.classList.toggle("hidden", cur !== steps.length - 1);
      window.scrollTo({ top: 0, behavior: "smooth" });
    };
    if (steps.length > 1) {
      const errStep = steps.findIndex((s) => $(".has-error", s));
      show(errStep >= 0 ? errStep : 0);
      $("[data-next]", wizard)?.addEventListener("click", () => { if (validate(steps[cur])) show(cur + 1); });
      $("[data-prev]", wizard)?.addEventListener("click", () => show(cur - 1));
    }
    wizard.addEventListener("submit", (e) => {
      applyConditions();
      for (let i = 0; i < steps.length; i++) {
        if (!validate(steps[i])) { e.preventDefault(); show(i); return; }
      }
      $$("[data-submit]", wizard).forEach((b) => { b.disabled = true; b.textContent = "Submitting…"; });
    });
    wizard.addEventListener("input", applyConditions);
    wizard.addEventListener("change", applyConditions);
    applyConditions();

    const title = $('[name="title"]', wizard), desc = $('[name="description"]', wizard), box = $("#similar");
    if (title && box && wizard.dataset.dupes !== "false") {
      const dismissed = new Set();
      const check = debounce(async () => {
        if (title.value.trim().length < 4) { box.classList.add("hidden"); return; }
        try {
          const qs = new URLSearchParams({ project, title: title.value, description: (desc?.value || "").slice(0, 4000) });
          const list = (await api("GET", "/api/v1/similar?" + qs)).filter((i) => !dismissed.has(i.key));
          if (!list.length) { box.classList.add("hidden"); return; }
          box.innerHTML = `<h4>Is it one of these? Voting on an existing report helps more than a new one.</h4><ul>${list.map((i) => `
            <li><span class="key">${esc(i.key)}</span>
            <a href="/issues/${esc(i.key)}" target="_blank" rel="noopener">${esc(i.title)}</a>
            <span class="badge" style="--c:#888">${esc(i.state.label || i.state.id)}${i.resolution ? " · " + esc(i.resolution.label || i.resolution.id) : ""}</span>
            <span class="muted small">${i.votes} votes · ${esc(i.reason)}</span>
            ${i.resolved_at ? "" : `<a class="btn btn-sm right" href="/issues/${esc(i.key)}#confirm">I have this too</a>`}
            </li>`).join("")}</ul>`;
          box.classList.remove("hidden");
        } catch { /* ignore */ }
      }, 350);
      title.addEventListener("input", check);
      desc?.addEventListener("input", check);
      if (title.value) check();
    }

    const result = $("#parse-result");
    if (result) {
      const texts = new Map();
      const run = debounce(async () => {
        const merged = [desc?.value || "", ...$$("textarea[data-parse]", wizard).map((t) => t.value), ...texts.values()].join("\n\n");
        if (merged.trim().length < 20) { result.classList.add("hidden"); return; }
        try {
          const r = await api("POST", "/api/v1/parse", { project, text: merged.slice(0, 2_000_000) });
          const parts = [];
          if (r.signature) parts.push(`<div>⚡ Crash signature: <code>${esc(r.signature)}</code> <span class="muted">(${esc(r.fingerprint)})</span></div>`);
          for (const [k, v] of Object.entries(r.fields || {})) {
            parts.push(`<div>Detected <b>${esc(k)}</b>: ${v.map(esc).join(", ")}</div>`);
            const el = $(`[name="f_${k}"]`, wizard);
            if (el && el.tagName === "SELECT" && !el.value && v[0]) { el.value = v[0]; el.dispatchEvent(new Event("change", { bubbles: true })); }
            else if (el && el.tagName === "INPUT" && el.type === "text" && !el.value) el.value = v[0];
          }
          if (r.labels?.length) parts.push(`<div>Labels: ${r.labels.map(esc).join(", ")}</div>`);
          if (!parts.length) { result.classList.add("hidden"); return; }
          result.innerHTML = `<b>We read your logs</b>${parts.join("")}<div class="muted small">Fields were pre-filled for you, please double-check them.</div>`;
          result.classList.remove("hidden");
        } catch { /* ignore */ }
      }, 600);
      wizard.addEventListener("input", (e) => { if (e.target.matches("textarea")) run(); });
      $$('input[type="file"]', wizard).forEach((inp) => inp.addEventListener("change", () => {
        texts.clear();
        Array.from(inp.files).filter((f) => f.size < 4 * 1024 * 1024 && /\.(log|txt|json|crash|xml|yml|yaml)$|^[^.]+$/i.test(f.name))
          .forEach((f) => f.text().then((t) => { texts.set(f.name, t); run(); }));
      }));
    }
  }

  const bulkBar = $("#bulk-form");
  const updateBulk = () => {
    if (!bulkBar) return;
    const n = $$(".issue-row .sel:checked").length;
    bulkBar.classList.toggle("hidden", n === 0);
    const c = $("[data-selected-count]", bulkBar); if (c) c.textContent = n;
  };
  $$(".issue-row .sel").forEach((cb) => cb.addEventListener("change", updateBulk));
  $("#select-all")?.addEventListener("change", (e) => { $$(".issue-row .sel").forEach((cb) => (cb.checked = e.target.checked)); updateBulk(); });
  const bulkCanned = $('#bulk-form select[name="transition"]');
  bulkCanned?.addEventListener("change", () => {
    const opt = bulkCanned.selectedOptions[0];
    const res = $("#bulk-resolution");
    if (res) res.classList.toggle("hidden", !opt || opt.dataset.closed !== "1");
  });

  $$("[data-transition-form]").forEach((f) => {
    const req = f.querySelector('[name="comment"][data-required]');
    f.addEventListener("submit", (e) => { if (req && !req.value.trim()) { e.preventDefault(); req.focus(); toast("A comment is required"); } });
  });

  const rows = () => $$(".issue-row");
  let focus = -1, gPending = false;
  const setFocus = (i) => {
    const r = rows(); if (!r.length) return;
    focus = Math.max(0, Math.min(i, r.length - 1));
    r.forEach((x, j) => x.classList.toggle("kb-focus", j === focus));
    r[focus].scrollIntoView({ block: "nearest" });
  };
  const typing = (el) => el && (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName));
  document.addEventListener("keydown", (e) => {
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    if (typing(e.target)) { if (e.key === "Escape") e.target.blur(); return; }
    const k = e.key;
    if (gPending) {
      gPending = false;
      const go = { h: "/", i: "/issues", n: "/notifications", t: "/triage", m: "/issues?q=reporter:me", w: "/issues?q=watching:me+is:open" }[k];
      if (go) { e.preventDefault(); location.href = go; }
      return;
    }
    switch (k) {
      case "/": e.preventDefault(); $("#global-search")?.focus(); break;
      case "g": gPending = true; setTimeout(() => (gPending = false), 1200); break;
      case "c": if ($('a[href="/new"]')) { e.preventDefault(); location.href = "/new"; } break;
      case "j": e.preventDefault(); setFocus(focus + 1); break;
      case "k": e.preventDefault(); setFocus(focus - 1); break;
      case "o": case "Enter": if (focus >= 0) { e.preventDefault(); location.href = $("a.title", rows()[focus]).href; } break;
      case "x": if (focus >= 0) { const cb = $(".sel", rows()[focus]); if (cb) { cb.checked = !cb.checked; updateBulk(); } } break;
      case "v": $("form[data-toggle=vote] button")?.click(); break;
      case "w": $("form[data-toggle=watch] button")?.click(); break;
      case "r": { const ta = $("#comment-body"); if (ta) { e.preventDefault(); ta.focus(); ta.scrollIntoView({ block: "center" }); } break; }
      case "e": { const a = $("a[data-edit]"); if (a) location.href = a.href; break; }
      case "d": { const d = $("#dup-pop"); if (d) { d.open = true; $("input", d)?.focus(); e.preventDefault(); } break; }
      case "?": e.preventDefault(); toggleHelp(); break;
      case "Escape": $(".kbd-help")?.remove(); break;
    }
  });
  function toggleHelp() {
    const ex = $(".kbd-help"); if (ex) return ex.remove();
    const rowsHTML = [["/", "Search"], ["c", "Report a bug"], ["j / k", "Next / previous issue"], ["o / Enter", "Open issue"], ["x", "Select issue (bulk)"],
    ["v", "Vote"], ["w", "Watch"], ["r", "Reply"], ["e", "Edit issue"], ["d", "Mark duplicate"], ["g h", "Home"], ["g i", "Issues"],
    ["g n", "Notifications"], ["g t", "Triage"], ["g m", "My reports"], ["Ctrl+Enter", "Submit comment"], ["?", "This help"]]
      .map(([a, b]) => `<tr><td>${a.split(" / ").map((x) => `<kbd>${x}</kbd>`).join(" / ")}</td><td>${b}</td></tr>`).join("");
    const d = document.createElement("div"); d.className = "kbd-help";
    d.innerHTML = `<div class="card"><div class="card-head"><h3>Keyboard shortcuts</h3><button class="btn btn-sm right" type="button">Close</button></div><div class="card-body"><table>${rowsHTML}</table></div></div>`;
    d.addEventListener("click", (e) => { if (e.target === d || e.target.tagName === "BUTTON") d.remove(); });
    document.body.appendChild(d);
  }

  const bell = $("#bell");
  if (bell) setInterval(async () => {
    if (document.hidden) return;
    try {
      const r = await api("GET", "/api/v1/notifications?unread=1");
      let c = $(".count", bell);
      if (r.unread && !c) { c = document.createElement("span"); c.className = "count"; bell.appendChild(c); }
      if (c) { if (r.unread) c.textContent = r.unread; else c.remove(); }
    } catch { /* ignore */ }
  }, 60000);
})();
