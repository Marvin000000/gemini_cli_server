#!/usr/bin/env node
const http = require("http");
const fs = require("fs");
const path = require("path");
const { spawn, spawnSync } = require("child_process");

const port = Number(process.argv[2] || process.env.PORT || 8765);



function runGemini(prompt) {
  return new Promise((resolve, reject) => {
    const args = ['--yolo', '--prompt', prompt, '-m', "gemini-2.5-flash"];
    const p = spawn("gemini", args, { stdio: ["ignore", "pipe", "pipe"] });
    let out = "", err = "";
    p.stdout.on("data", d => (out += d.toString()));
    p.stderr.on("data", d => (err += d.toString()));
    p.on("close", code => (code === 0 ? resolve(out.trim()) : reject(new Error(err || `exit ${code}`))));
  });
}

const server = http.createServer((req, res) => {
  if (req.method === "GET" && req.url === "/health") {
    res.writeHead(200, { "Content-Type": "text/plain" });
    return res.end("ok");
  }

  if (req.method === "POST" && req.url.startsWith("/event")) {
    let raw = "";
    req.on("data", c => (raw += c));
    req.on("end", async () => {
      let parsed;
      try {
        parsed = JSON.parse(raw || "{}");
      } catch {
        parsed = { raw };
      }

      const source = parsed.source || "unknown";
      const message = parsed.message || "";

      if (!message) {
        res.writeHead(400, { "Content-Type": "application/json" });
        return res.end(JSON.stringify({ ok: false, error: "No message provided" }));
      }



      try {
        const reply = await runGemini(message);
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ ok: true, reply }));
      } catch (e) {
        res.writeHead(500, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ ok: false, error: String(e.message || e) }));
      }
    });
    return;
  }

  res.writeHead(404, { "Content-Type": "text/plain" });
  res.end("Not found");
});

function cleanup() {
  server.close(() => process.exit(0));
  setTimeout(() => process.exit(0), 1500).unref();
}

server.listen(port, () => {
  console.log(`Server listening on http://127.0.0.1:${port}/event`);
  console.log(`Gemini CLI working directory: ${process.cwd()}`);
});

process.on("SIGINT", cleanup);
process.on("SIGTERM", cleanup);
