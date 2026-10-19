// Replay viewer. The server stores only seed + inputs for each match and
// re-simulates them into frames on request (GET /api/replays/{id}); this page
// just steps through those frames. "verified" means the re-simulation reached
// exactly the final state recorded when the match ended.

import { createBoard, renderScores, escapeHtml } from "./render.js";

const board = createBoard(document.getElementById("board"));
const $ = (id) => document.getElementById(id);
const matchesEl = $("matches");
const scoresEl = $("scores");
const playBtn = $("play");
const seek = $("seek");

let playback = null; // {meta, verified, frames}
let pos = 0;         // index into playback.frames
let playing = false;
let timer = null;

async function loadList() {
  const res = await fetch("/api/replays");
  const list = await res.json();
  if (!list.length) {
    matchesEl.innerHTML = `<div class="score" style="color:#8b98a5">no matches yet — play one in the live arena; it is saved when every snake is dead</div>`;
    return;
  }
  matchesEl.innerHTML = list.map((m) => {
    const who = m.players.map((p) => `${escapeHtml(p.name)} ${p.score}`).join(", ");
    const when = new Date(m.endedAt).toLocaleString();
    return `<button class="match" data-id="${escapeHtml(m.id)}">${who}` +
      `<div class="when">${escapeHtml(when)} · room ${m.room} · ${m.ticks} ticks</div></button>`;
  }).join("");
  for (const btn of matchesEl.querySelectorAll(".match")) {
    btn.addEventListener("click", () => open(btn.dataset.id));
  }
}

async function open(id) {
  pause();
  for (const b of matchesEl.querySelectorAll(".match")) {
    b.classList.toggle("active", b.dataset.id === id);
  }
  const res = await fetch(`/api/replays/${encodeURIComponent(id)}`);
  if (!res.ok) {
    $("facts").textContent = `could not load replay (${res.status})`;
    return;
  }
  playback = await res.json();
  const m = playback.meta;
  board.resize({ width: m.width, height: m.height });
  seek.max = String(playback.frames.length - 1);
  for (const el of [playBtn, $("back"), $("fwd"), seek]) el.disabled = false;

  const badge = playback.verified
    ? `<span class="badge ok">verified</span>`
    : `<span class="badge bad">mismatch: inputs do not reproduce the recorded result</span>`;
  const framesKB = (JSON.stringify(playback.frames).length / 1024).toFixed(0);
  $("facts").innerHTML = `${badge} stored ${(m.storedBytes / 1024).toFixed(1)} KB ` +
    `(seed + ${m.ticks} inputs) → ${framesKB} KB of frames re-simulated · seed ${m.seed}`;
  show(0);
}

function show(i) {
  if (!playback) return;
  pos = Math.max(0, Math.min(i, playback.frames.length - 1));
  const f = playback.frames[pos];
  board.draw(f, null);
  renderScores(scoresEl, f, null, "no snakes yet");
  seek.value = String(pos);
  $("tick").textContent = `tick ${f.tick} / ${playback.frames[playback.frames.length - 1].tick}`;
  if (pos === playback.frames.length - 1) pause();
}

function play() {
  if (!playback) return;
  if (pos === playback.frames.length - 1) show(0); // replay from the start
  playing = true;
  playBtn.textContent = "Pause";
  const ms = 1000 / (playback.meta.tickRate || 10) / Number($("speed").value);
  timer = setInterval(() => show(pos + 1), ms);
}

function pause() {
  playing = false;
  playBtn.textContent = "Play";
  clearInterval(timer);
}

playBtn.addEventListener("click", () => (playing ? pause() : play()));
$("back").addEventListener("click", () => { pause(); show(pos - 1); });
$("fwd").addEventListener("click", () => { pause(); show(pos + 1); });
seek.addEventListener("input", () => { pause(); show(Number(seek.value)); });
$("speed").addEventListener("change", () => { if (playing) { pause(); play(); } });

window.addEventListener("keydown", (e) => {
  if (!playback) return;
  if (e.key === " ") { e.preventDefault(); playing ? pause() : play(); }
  if (e.key === "ArrowLeft") { pause(); show(pos - 1); }
  if (e.key === "ArrowRight") { pause(); show(pos + 1); }
});
window.addEventListener("resize", () => { board.resize(); show(pos); });

board.resize();
board.draw({ snakes: [], food: [] }, null);
loadList();
