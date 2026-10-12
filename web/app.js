// Browser client for Realtime Arena.
//
// The client is deliberately "dumb": it sends only intent (join + direction)
// and renders whatever snapshots the authoritative server sends back. It never
// simulates movement itself, so it can never disagree with the server. On a
// dropped connection it reconnects and replays its reconnect token to reclaim
// its snake.

const COLORS = [
  "#4cc38a", "#36a3f7", "#ffb454", "#f07178",
  "#c792ea", "#7fdbca", "#ff9e64", "#73d0ff",
];

const canvas = document.getElementById("board");
const ctx = canvas.getContext("2d");
const scoresEl = document.getElementById("scores");
const statusEl = document.getElementById("status");

let cell = 18;          // pixels per grid cell, set from the server config
let cfg = { width: 40, height: 30 };
let myId = null;
let state = { snakes: [], food: [] };
let ws = null;
let reconnectTimer = null;

const myName = localStorage.getItem("arena.name") ||
  "player-" + Math.floor(Math.random() * 1000);
localStorage.setItem("arena.name", myName);

function wsURL() {
  const proto = location.protocol === "https:" ? "wss" : "ws";
  return `${proto}://${location.host}/ws`;
}

function connect() {
  setStatus("connecting…", "");
  ws = new WebSocket(wsURL());

  ws.onopen = () => {
    const token = localStorage.getItem("arena.token");
    if (token) {
      ws.send(JSON.stringify({ type: "rejoin", token }));
      // If the slot expired the server replies with an error; we then join.
    } else {
      ws.send(JSON.stringify({ type: "join", name: myName }));
    }
  };

  ws.onmessage = (ev) => handleMessage(JSON.parse(ev.data));

  ws.onclose = () => {
    setStatus("disconnected — reconnecting…", "down");
    scheduleReconnect();
  };
  ws.onerror = () => ws.close();
}

function scheduleReconnect() {
  if (reconnectTimer) return;
  reconnectTimer = setTimeout(() => {
    reconnectTimer = null;
    connect();
  }, 800);
}

function handleMessage(msg) {
  switch (msg.type) {
    case "welcome":
      myId = msg.playerId;
      localStorage.setItem("arena.token", msg.token);
      cfg = { width: msg.width, height: msg.height };
      resizeBoard();
      setStatus("connected", "live");
      break;
    case "snapshot":
      state = msg;
      break;
    case "error":
      // Most likely an expired reconnect token: start a fresh game.
      if (msg.msg && msg.msg.indexOf("rejoin") >= 0) {
        localStorage.removeItem("arena.token");
        ws.send(JSON.stringify({ type: "join", name: myName }));
      }
      break;
  }
}

function resizeBoard() {
  // Fit the arena into a sensible on-screen size while keeping square cells.
  const maxW = Math.min(720, window.innerWidth - 40);
  cell = Math.max(6, Math.floor(maxW / cfg.width));
  canvas.width = cfg.width * cell;
  canvas.height = cfg.height * cell;
}

function colorFor(id) {
  return COLORS[((id % COLORS.length) + COLORS.length) % COLORS.length];
}

function draw() {
  ctx.fillStyle = "#1a222c";
  ctx.fillRect(0, 0, canvas.width, canvas.height);

  // Grid.
  ctx.strokeStyle = "#1e2730";
  ctx.lineWidth = 1;
  for (let x = 0; x <= cfg.width; x++) {
    ctx.beginPath();
    ctx.moveTo(x * cell + 0.5, 0);
    ctx.lineTo(x * cell + 0.5, canvas.height);
    ctx.stroke();
  }
  for (let y = 0; y <= cfg.height; y++) {
    ctx.beginPath();
    ctx.moveTo(0, y * cell + 0.5);
    ctx.lineTo(canvas.width, y * cell + 0.5);
    ctx.stroke();
  }

  // Food.
  ctx.fillStyle = "#ffb454";
  for (const f of state.food || []) {
    circle(f.x * cell + cell / 2, f.y * cell + cell / 2, cell * 0.32);
  }

  // Snakes.
  for (const s of state.snakes || []) {
    const base = colorFor(s.id);
    ctx.fillStyle = s.alive ? base : "#3a4652";
    for (let i = 0; i < s.body.length; i++) {
      const p = s.body[i];
      const pad = i === 0 ? 1 : 2; // head slightly larger
      roundRect(p.x * cell + pad, p.y * cell + pad, cell - pad * 2, cell - pad * 2, 3);
    }
    // Ring the local player's head so you can find yourself.
    if (s.id === myId && s.body.length) {
      const h = s.body[0];
      ctx.strokeStyle = "#ffffff";
      ctx.lineWidth = 2;
      ctx.strokeRect(h.x * cell + 1, h.y * cell + 1, cell - 2, cell - 2);
    }
  }

  renderScores();
  requestAnimationFrame(draw);
}

function circle(cx, cy, r) {
  ctx.beginPath();
  ctx.arc(cx, cy, r, 0, Math.PI * 2);
  ctx.fill();
}

function roundRect(x, y, w, h, r) {
  if (w < 0 || h < 0) return;
  r = Math.min(r, w / 2, h / 2);
  ctx.beginPath();
  ctx.moveTo(x + r, y);
  ctx.arcTo(x + w, y, x + w, y + h, r);
  ctx.arcTo(x + w, y + h, x, y + h, r);
  ctx.arcTo(x, y + h, x, y, r);
  ctx.arcTo(x, y, x + w, y, r);
  ctx.fill();
}

function renderScores() {
  const snakes = (state.snakes || []).slice().sort((a, b) => b.score - a.score);
  scoresEl.innerHTML = snakes.map((s) => {
    const me = s.id === myId ? " me" : "";
    const dead = s.alive ? "" : " (dead)";
    return `<div class="score${me}">` +
      `<span><span class="dot" style="background:${colorFor(s.id)}"></span>` +
      `${escapeHtml(s.name)}${dead}</span><span>${s.score}</span></div>`;
  }).join("") || `<div class="score" style="color:#8b98a5">waiting for players…</div>`;
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

function setStatus(text, cls) {
  statusEl.textContent = text;
  statusEl.className = "status" + (cls ? " " + cls : "");
}

const KEY_TO_DIR = {
  ArrowUp: "up", ArrowDown: "down", ArrowLeft: "left", ArrowRight: "right",
  w: "up", s: "down", a: "left", d: "right",
  W: "up", S: "down", A: "left", D: "right",
};

window.addEventListener("keydown", (e) => {
  const dir = KEY_TO_DIR[e.key];
  if (!dir || !ws || ws.readyState !== WebSocket.OPEN) return;
  e.preventDefault();
  ws.send(JSON.stringify({ type: "input", dir }));
});

window.addEventListener("resize", resizeBoard);

resizeBoard();
connect();
requestAnimationFrame(draw);
