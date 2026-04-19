// Auto-dismiss flash messages
setTimeout(function() {
    var flash = document.getElementById('flash');
    if (flash) flash.style.display = 'none';
}, 5000);

// Add CSRF token to HTMX requests
document.body.addEventListener('htmx:configRequest', function(event) {
    var token = document.querySelector('meta[name="csrf-token"]').getAttribute('content');
    event.detail.headers['X-CSRF-Token'] = token;
});

// Port forward management on VM create form
(function() {
    var addBtn = document.getElementById('add-port-forward');
    var container = document.getElementById('port-forwards');
    if (!addBtn || !container) return;

    addBtn.addEventListener('click', function() {
        var row = document.createElement('div');
        row.className = 'flex items-center space-x-2 mb-2';
        row.innerHTML = '<input type="number" name="host_port" min="1" max="65535" placeholder="Host port" class="w-1/3 px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500 text-sm">' +
            '<span class="text-gray-400">:</span>' +
            '<input type="number" name="guest_port" min="1" max="65535" placeholder="Guest port" class="w-1/3 px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500 text-sm">' +
            '<select name="protocol" class="px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500 text-sm"><option value="tcp">TCP</option><option value="udp">UDP</option></select>' +
            '<button type="button" class="remove-port-forward text-red-500 hover:text-red-700 text-sm px-2" title="Remove">\u2715</button>';
        container.appendChild(row);
    });

    container.addEventListener('click', function(e) {
        if (e.target.classList.contains('remove-port-forward')) {
            e.target.closest('.flex').remove();
        }
    });
})();

// Copy API key to clipboard
function copyKey() {
    var key = document.getElementById('api-key').textContent;
    navigator.clipboard.writeText(key).then(function() {
        var btn = document.getElementById('copy-btn');
        btn.textContent = 'Copied!';
        btn.classList.remove('bg-blue-600', 'hover:bg-blue-700');
        btn.classList.add('bg-green-600');
        setTimeout(function() {
            btn.textContent = 'Copy';
            btn.classList.remove('bg-green-600');
            btn.classList.add('bg-blue-600', 'hover:bg-blue-700');
        }, 2000);
    });
}
