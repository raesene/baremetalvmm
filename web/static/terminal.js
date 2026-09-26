document.addEventListener("DOMContentLoaded", function () {
    var container = document.getElementById("terminal-container");
    if (!container || !container.dataset.vmName) return;
    // xterm measures glyph cells when it opens, so wait for the webfont.
    var start = function () { initTerminal(container.dataset.vmName); };
    if (document.fonts && document.fonts.load) {
        document.fonts.load('14px "Azeret Mono"').then(start, start);
    } else {
        start();
    }
});

function initTerminal(vmName) {
    var statusEl = document.getElementById("terminal-status");
    var statusText = document.getElementById("terminal-status-text");

    var term = new Terminal({
        cursorBlink: true,
        fontSize: 14,
        fontFamily: "'Azeret Mono', ui-monospace, Menlo, monospace",
        theme: {
            background: "#101318",
            foreground: "#E7E1D4",
            cursor: "#E7A84B",
            cursorAccent: "#101318",
            selectionBackground: "rgba(155, 140, 255, 0.35)",
            black: "#1F2630",
            red: "#EF6F6C",
            green: "#79D18B",
            yellow: "#F0C35B",
            blue: "#6BC7F1",
            magenta: "#9B8CFF",
            cyan: "#5BC8D6",
            white: "#E7E1D4",
            brightBlack: "#8D96A3",
            brightRed: "#F59B99",
            brightGreen: "#9FE0AC",
            brightYellow: "#F5D488",
            brightBlue: "#9AD8F5",
            brightMagenta: "#BDB3FF",
            brightCyan: "#8AD9E3",
            brightWhite: "#FFFFFF"
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
        statusText.textContent = "connected";

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
        statusText.textContent = "disconnected";
        term.write("\r\n\x1b[31mConnection closed.\x1b[0m\r\n");
    });

    ws.addEventListener("error", function () {
        statusEl.className = "terminal-status status-disconnected";
        statusText.textContent = "connection error";
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
