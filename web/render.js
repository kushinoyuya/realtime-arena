// Board rendering shared by the live client (app.js) and the replay viewer
// (replay.js). Both draw the same shape of state — the server's snapshots and
// the replay frames use one wire format — so there is one drawing routine.

const COLORS = [
  "#4cc38a", "#36a3f7", "#ffb454", "#f07178",
  "#c792ea", "#7fdbca", "#ff9e64", "#73d0ff",
];

export function colorFor(id) {
  return COLORS[((id % COLORS.length) + COLORS.length) % COLORS.length];
}

export function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

// createBoard binds a canvas. resize() fits it to an arena size; draw() paints
// one state ({snakes, food}), ringing the head of snake `highlightId`.
export function createBoard(canvas) {
  const ctx = canvas.getContext("2d");
  let cell = 18; // pixels per grid cell
  let cfg = { width: 40, height: 30 };

  function resize(next) {
    if (next) cfg = { width: next.width, height: next.height };
    // Fit the arena into a sensible on-screen size while keeping square cells.
    const maxW = Math.min(720, window.innerWidth - 40);
    cell = Math.max(6, Math.floor(maxW / cfg.width));
    canvas.width = cfg.width * cell;
    canvas.height = cfg.height * cell;
  }

  function draw(state, highlightId) {
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
      if (s.id === highlightId && s.body.length) {
        const h = s.body[0];
        ctx.strokeStyle = "#ffffff";
        ctx.lineWidth = 2;
        ctx.strokeRect(h.x * cell + 1, h.y * cell + 1, cell - 2, cell - 2);
      }
    }
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

  return { resize, draw };
}

// renderScores fills el with the scoreboard for state's snakes.
export function renderScores(el, state, highlightId, emptyText) {
  const snakes = (state.snakes || []).slice().sort((a, b) => b.score - a.score);
  el.innerHTML = snakes.map((s) => {
    const me = s.id === highlightId ? " me" : "";
    const dead = s.alive ? "" : " (dead)";
    return `<div class="score${me}">` +
      `<span><span class="dot" style="background:${colorFor(s.id)}"></span>` +
      `${escapeHtml(s.name)}${dead}</span><span>${s.score}</span></div>`;
  }).join("") || `<div class="score" style="color:#8b98a5">${escapeHtml(emptyText)}</div>`;
}
