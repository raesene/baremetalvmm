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
