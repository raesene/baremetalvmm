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

// Live VM state: /events emits {"name":..., "state":...} whenever a VM changes
// state. Update any status chip for that VM on the page and, on the dashboard,
// append a line to the activity log.
(function() {
    var statusEl = document.getElementById('event-status');
    if (!statusEl || !window.EventSource) return;
    var statusText = document.getElementById('event-status-text');
    var log = document.getElementById('event-log');
    var logChip = document.getElementById('log-chip');
    var levels = { running: 'ok', created: 'info', stopped: 'info', starting: 'warn', stopping: 'warn', error: 'err', deleted: 'warn' };

    function setLive(live) {
        statusEl.classList.toggle('is-live', live);
        statusEl.classList.toggle('is-down', !live);
        statusText.textContent = live ? 'event stream live' : 'event stream reconnecting';
        if (logChip) logChip.className = live ? 'chip chip-running' : 'chip chip-error';
    }

    function updateChips(name, state) {
        document.querySelectorAll('[data-vm]').forEach(function(el) {
            if (el.getAttribute('data-vm') !== name) return;
            var chip = el.querySelector('[data-state]');
            if (!chip) return;
            chip.className = 'chip chip-' + state;
            var label = chip.querySelector('[data-state-label]');
            if (label) label.textContent = state;
        });
    }

    function appendLog(name, state) {
        if (!log) return;
        var empty = log.querySelector('.log-empty');
        if (empty) empty.remove();

        var li = document.createElement('li');
        li.className = 'is-new';
        var ts = document.createElement('span');
        ts.className = 'ts';
        ts.textContent = new Date().toLocaleTimeString([], { hour12: false });
        var lvl = document.createElement('span');
        var level = levels[state] || 'info';
        lvl.className = 'lvl-' + level;
        lvl.textContent = '[' + level.toUpperCase() + ']';
        var obj = document.createElement('span');
        obj.className = 'obj';
        var vmName = document.createElement('span');
        vmName.className = 'tok-data';
        vmName.textContent = name;
        obj.appendChild(vmName);
        obj.appendChild(document.createTextNode(' → ' + state));
        li.appendChild(ts);
        li.appendChild(lvl);
        li.appendChild(obj);
        log.insertBefore(li, log.firstChild);
        while (log.children.length > 50) log.removeChild(log.lastChild);
    }

    var source = new EventSource('/events');
    source.addEventListener('open', function() { setLive(true); });
    source.addEventListener('error', function() { setLive(false); });
    source.addEventListener('vm-update', function(e) {
        var msg;
        try { msg = JSON.parse(e.data); } catch (err) { return; }
        if (!msg || !msg.name || !msg.state) return;
        updateChips(msg.name, msg.state);
        appendLog(msg.name, msg.state);
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
