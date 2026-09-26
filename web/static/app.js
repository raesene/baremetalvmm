// Auto-dismiss flash messages
setTimeout(function() {
    var flash = document.getElementById('flash');
    if (!flash) return;
    flash.classList.add('is-fading');
    setTimeout(function() { flash.remove(); }, 400);
}, 5000);

// Add CSRF token to HTMX requests
document.body.addEventListener('htmx:configRequest', function(event) {
    var token = document.querySelector('meta[name="csrf-token"]').getAttribute('content');
    event.detail.headers['X-CSRF-Token'] = token;
});

// Live activity: /events emits one JSON object per VM or cluster change
// ({time, kind, name, state, source, detail, level}). Update any status chip
// for that object on the page and, on the dashboard, prepend it to the
// activity log, which the server renders with recent history on page load.
(function() {
    var statusEl = document.getElementById('event-status');
    if (!statusEl || !window.EventSource) return;
    var statusText = document.getElementById('event-status-text');
    var log = document.getElementById('event-log');
    var logChip = document.getElementById('log-chip');
    var chipStates = ['created', 'starting', 'running', 'stopping', 'stopped', 'error', 'creating', 'deleted'];

    function clock(date) {
        return date.toLocaleTimeString([], { hour12: false });
    }

    // Show server-rendered timestamps in the browser's timezone.
    if (log) {
        log.querySelectorAll('time[datetime]').forEach(function(t) {
            var d = new Date(t.getAttribute('datetime'));
            if (!isNaN(d)) t.textContent = clock(d);
        });
    }

    function setLive(live) {
        statusEl.classList.toggle('is-live', live);
        statusEl.classList.toggle('is-down', !live);
        statusText.textContent = live ? 'event stream live' : 'event stream reconnecting';
        if (logChip) logChip.className = live ? 'chip chip-running' : 'chip chip-error';
    }

    function updateChips(ev) {
        if (chipStates.indexOf(ev.state) === -1) return;
        var attr = 'data-' + ev.kind;
        document.querySelectorAll('[' + attr + ']').forEach(function(el) {
            if (el.getAttribute(attr) !== ev.name) return;
            var chip = el.querySelector('[data-state]');
            if (!chip) return;
            chip.className = 'chip chip-' + ev.state;
            var label = chip.querySelector('[data-state-label]');
            if (label) label.textContent = ev.state;
        });
    }

    function span(cls, text) {
        var el = document.createElement('span');
        el.className = cls;
        el.textContent = text;
        return el;
    }

    function appendLog(ev) {
        if (!log) return;
        var empty = log.querySelector('.log-empty');
        if (empty) empty.remove();

        var li = document.createElement('li');
        li.className = 'is-new';
        var ts = document.createElement('time');
        ts.className = 'ts';
        ts.setAttribute('datetime', ev.time);
        ts.textContent = clock(new Date(ev.time));
        var obj = span('obj', '');
        if (ev.kind === 'cluster') {
            obj.appendChild(span('kind', 'cluster'));
            obj.appendChild(document.createTextNode(' '));
        }
        obj.appendChild(span('tok-data', ev.name));
        obj.appendChild(document.createTextNode(' \u2192 ' + ev.state));
        li.appendChild(ts);
        li.appendChild(span('lvl-' + ev.level, '[' + String(ev.level).toUpperCase() + ']'));
        li.appendChild(obj);
        li.appendChild(span('src', ev.source));
        if (ev.detail) li.appendChild(span('detail', ev.detail));
        log.insertBefore(li, log.firstChild);
        while (log.children.length > 50) log.removeChild(log.lastChild);
    }

    var source = new EventSource('/events');
    source.addEventListener('open', function() { setLive(true); });
    source.addEventListener('error', function() { setLive(false); });
    source.addEventListener('activity', function(e) {
        var ev;
        try { ev = JSON.parse(e.data); } catch (err) { return; }
        if (!ev || !ev.kind || !ev.name || !ev.state) return;
        updateChips(ev);
        appendLog(ev);
    });
})();

// Port forward management on VM create form
(function() {
    var addBtn = document.getElementById('add-port-forward');
    var container = document.getElementById('port-forwards');
    if (!addBtn || !container) return;

    addBtn.addEventListener('click', function() {
        var row = document.createElement('div');
        row.className = 'pf-row';
        row.innerHTML = '<input type="number" name="host_port" min="1" max="65535" placeholder="host port" class="input">' +
            '<span class="pf-sep">→</span>' +
            '<input type="number" name="guest_port" min="1" max="65535" placeholder="guest port" class="input">' +
            '<select name="protocol" class="input"><option value="tcp">tcp</option><option value="udp">udp</option></select>' +
            '<button type="button" class="pf-remove" title="Remove">✕</button>';
        container.appendChild(row);
    });

    container.addEventListener('click', function(e) {
        if (e.target.classList.contains('pf-remove')) {
            e.target.closest('.pf-row').remove();
        }
    });
})();

// Confirm dialogs for data-confirm buttons
document.addEventListener('submit', function(e) {
    var btn = e.submitter || e.target.querySelector('[data-confirm]');
    if (btn && btn.getAttribute('data-confirm')) {
        if (!confirm(btn.getAttribute('data-confirm'))) {
            e.preventDefault();
        }
    }
});

// Show a busy state on long-running action buttons (snapshot create/restore).
// Runs after the confirm handler; skips if that handler cancelled submission.
document.addEventListener('submit', function(e) {
    if (e.defaultPrevented) return;
    var btn = e.submitter;
    if (btn && btn.getAttribute('data-busy')) {
        btn.disabled = true;
        btn.textContent = btn.getAttribute('data-busy');
        btn.classList.add('is-busy');
    }
});

// Show a busy state on download buttons
document.addEventListener('submit', function(e) {
    if (!e.target.classList.contains('download-form')) return;
    var btn = e.target.querySelector('.download-btn');
    if (btn) {
        btn.disabled = true;
        btn.innerHTML = '<span class="spinner"></span> Downloading';
        btn.classList.add('is-busy');
    }
});

// Cluster create: toggle fields based on cluster type (kubeadm vs openshift)
(function() {
    var typeSel = document.getElementById('cluster_type');
    if (!typeSel) return;
    var kubeadmEls = document.querySelectorAll('.kubeadm-only');
    var openshiftEls = document.querySelectorAll('.openshift-only');
    var imageSel = document.getElementById('image');
    var kernelSel = document.getElementById('kernel');
    var cpus = document.getElementById('cpus');
    var memory = document.getElementById('memory');
    var disk = document.getElementById('disk');

    function selectKernel(name) {
        if (!kernelSel) return;
        for (var i = 0; i < kernelSel.options.length; i++) {
            if (kernelSel.options[i].value === name) { kernelSel.value = name; return; }
        }
    }

    function apply() {
        var isOcp = typeSel.value === 'openshift';
        kubeadmEls.forEach(function(el) { el.classList.toggle('hidden', isOcp); });
        openshiftEls.forEach(function(el) { el.classList.toggle('hidden', !isOcp); });
        // A disabled control is not submitted, so the hidden image select won't
        // be sent (and won't trip its required attribute) for OpenShift.
        if (imageSel) { imageSel.disabled = isOcp; imageSel.required = !isOcp; }
        // Pick sensible defaults for the selected type.
        selectKernel(isOcp ? 'security-kernel' : 'k8s-kernel');
        if (cpus) cpus.value = isOcp ? 4 : 2;
        if (memory) memory.value = isOcp ? 8192 : 4096;
        if (disk) disk.value = isOcp ? 20480 : 10240;
    }

    typeSel.addEventListener('change', apply);
    apply();
})();

// Copy API key to clipboard
(function() {
    var btn = document.getElementById('copy-btn');
    if (!btn) return;
    btn.addEventListener('click', function() {
        var key = document.getElementById('api-key').textContent;
        navigator.clipboard.writeText(key).then(function() {
            btn.textContent = 'Copied';
            btn.classList.add('is-done');
            setTimeout(function() {
                btn.textContent = 'Copy';
                btn.classList.remove('is-done');
            }, 2000);
        });
    });
})();
