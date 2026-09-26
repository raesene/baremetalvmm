// Copy buttons for code blocks: copies only the commands (lines starting
// with the "$ " prompt), without the prompt, comments or sample output.
document.querySelectorAll('.code-wrap').forEach(function(wrap) {
    var pre = wrap.querySelector('pre');
    if (!pre || !navigator.clipboard) return;
    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'copy';
    btn.textContent = 'copy';
    btn.setAttribute('aria-label', 'Copy commands');
    btn.addEventListener('click', function() {
        var lines = pre.innerText.split('\n')
            .filter(function(l) { return l.indexOf('$ ') === 0; })
            .map(function(l) { return l.slice(2).replace(/\s+#.*$/, ''); });
        navigator.clipboard.writeText(lines.join('\n')).then(function() {
            btn.textContent = 'copied';
            btn.classList.add('is-done');
            setTimeout(function() {
                btn.textContent = 'copy';
                btn.classList.remove('is-done');
            }, 1600);
        });
    });
    wrap.appendChild(btn);
});

// Highlight the nav entry for the section currently in view.
(function() {
    var links = {};
    document.querySelectorAll('.nav a[href^="#"]').forEach(function(a) {
        links[a.getAttribute('href').slice(1)] = a;
    });
    if (!('IntersectionObserver' in window)) return;
    var observer = new IntersectionObserver(function(entries) {
        entries.forEach(function(entry) {
            var link = links[entry.target.id];
            if (!link || !entry.isIntersecting) return;
            Object.keys(links).forEach(function(k) { links[k].classList.remove('is-active'); });
            link.classList.add('is-active');
        });
    }, { rootMargin: '-45% 0px -50% 0px' });
    Object.keys(links).forEach(function(id) {
        var el = document.getElementById(id);
        if (el) observer.observe(el);
    });
})();
