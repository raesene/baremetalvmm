document.addEventListener("DOMContentLoaded", function () {
    var container = document.getElementById("terminal-container");
    if (container && container.dataset.vmName) {
        initTerminal(container.dataset.vmName);
    }
});

function initTerminal(vmName) {
    var statusEl = document.getElementById("terminal-status");
    var statusText = document.getElementById("terminal-status-text");

    var term = new Terminal({
        cursorBlink: true,
        fontSize: 14,
        fontFamily: "'Cascadia Code', 'Fira Code', 'JetBrains Mono', Menlo, monospace",
        theme: {
            background: "#1a1a2e",
            foreground: "#e6e6e6",
            cursor: "#e6e6e6",
            selectionBackground: "#3d3d5c"
        },
        scrollback: 5000
    });

    var fitAddon = new FitAddon.FitAddon();
    term.loadAddon(fitAddon);

    var webLinksAddon = new WebLinksAddon.WebLinksAddon();
    term.loadAddon(webLinksAddon);

    term.open(document.getElementById("terminal-container"));
    fitAddon.fit();

    var protocol = location.protocol === "https:" ? "wss:" : "ws:";
    var wsUrl = protocol + "//" + location.host + "/ws/vms/" + vmName + "/terminal";

    var ws = new WebSocket(wsUrl);
    ws.binaryType = "arraybuffer";

    ws.addEventListener("open", function () {
        statusEl.className = "terminal-status status-connected";
        statusText.textContent = "Connected to " + vmName;

        ws.send(JSON.stringify({
            type: "resize",
            cols: term.cols,
            rows: term.rows
        }));

        term.focus();
    });

    ws.addEventListener("message", function (event) {
        if (event.data instanceof ArrayBuffer) {
            term.write(new Uint8Array(event.data));
        } else {
            term.write(event.data);
        }
    });

    ws.addEventListener("close", function () {
        statusEl.className = "terminal-status status-disconnected";
        statusText.textContent = "Disconnected from " + vmName;
        term.write("\r\n\x1b[31mConnection closed.\x1b[0m\r\n");
    });

    ws.addEventListener("error", function () {
        statusEl.className = "terminal-status status-disconnected";
        statusText.textContent = "Connection error";
    });

    term.onData(function (data) {
        if (ws.readyState === WebSocket.OPEN) {
            ws.send(new TextEncoder().encode(data));
        }
    });

    term.onResize(function (size) {
        if (ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify({
                type: "resize",
                cols: size.cols,
                rows: size.rows
            }));
        }
    });

    window.addEventListener("resize", function () {
        fitAddon.fit();
    });
}
