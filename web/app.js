// Browser client for Realtime Arena.
//
// The client is deliberately "dumb": it sends only intent (join + direction)
// and renders whatever snapshots the authoritative server sends back. It never
// simulates movement itself, so it can never disagree with the server. On a
// dropped connection it reconnects and replays its reconnect token to reclaim
// its snake.

import { createBoard, renderScores } from "./render.js";

const canvas = document.getElementById("board");
const board = createBoard(canvas);
const scoresEl = document.getElementById("scores");
const statusEl = document.getElementById("status");

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
      board.resize({ width: msg.width, height: msg.height });
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

function frame() {
  board.draw(state, myId); // ring the local player's head so you can find yourself
  renderScores(scoresEl, state, myId, "waiting for players…");
  requestAnimationFrame(frame);
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

window.addEventListener("resize", () => board.resize());

board.resize();
connect();
requestAnimationFrame(frame);
