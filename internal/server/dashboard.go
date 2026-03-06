package server

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

const dashboardHTML = `<!DOCTYPE html>
<html>
<head>
<title>Symphony Dashboard</title>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; margin: 20px; background: #0d1117; color: #c9d1d9; }
  h1 { color: #58a6ff; }
  .card { background: #161b22; border: 1px solid #30363d; border-radius: 6px; padding: 16px; margin: 12px 0; }
  table { width: 100%%; border-collapse: collapse; }
  th, td { text-align: left; padding: 8px 12px; border-bottom: 1px solid #21262d; }
  th { color: #8b949e; font-weight: 600; }
  .status-running { color: #3fb950; }
  .status-retrying { color: #d29922; }
  .meta { color: #8b949e; font-size: 0.9em; }
  #error { color: #f85149; display: none; }
</style>
</head>
<body>
<h1>Symphony</h1>
<div id="error"></div>
<div class="card">
  <h3>Polling</h3>
  <div id="polling" class="meta">Loading...</div>
</div>
<div class="card">
  <h3>Running Agents</h3>
  <table>
    <thead><tr><th>Issue</th><th>State</th><th>Session</th><th>Tokens</th><th>Turns</th><th>Runtime</th></tr></thead>
    <tbody id="running"></tbody>
  </table>
</div>
<div class="card">
  <h3>Retry Queue</h3>
  <table>
    <thead><tr><th>Issue</th><th>Attempt</th><th>Due In</th><th>Error</th></tr></thead>
    <tbody id="retrying"></tbody>
  </table>
</div>
<div class="card">
  <h3>Totals</h3>
  <div id="totals" class="meta">Loading...</div>
</div>
<script>
async function refresh() {
  try {
    const res = await fetch('/api/status');
    if (!res.ok) throw new Error(res.statusText);
    const data = await res.json();
    document.getElementById('error').style.display = 'none';
    renderPolling(data.polling);
    renderRunning(data.running || []);
    renderRetrying(data.retrying || []);
    renderTotals(data.totals);
  } catch (e) {
    document.getElementById('error').textContent = 'Failed to fetch: ' + e.message;
    document.getElementById('error').style.display = 'block';
  }
}
function renderPolling(p) {
  if (!p) return;
  const el = document.getElementById('polling');
  const status = p.checking ? 'Checking now...' : 'Next poll in ' + Math.round(p.next_poll_in_ms/1000) + 's';
  el.textContent = status + ' (interval: ' + (p.poll_interval_ms/1000) + 's)';
}
function renderRunning(items) {
  const tbody = document.getElementById('running');
  if (items.length === 0) { tbody.innerHTML = '<tr><td colspan="6" class="meta">No active agents</td></tr>'; return; }
  tbody.innerHTML = items.map(r => '<tr>' +
    '<td>' + r.identifier + '</td>' +
    '<td class="status-running">' + r.state + '</td>' +
    '<td class="meta">' + (r.session_id || 'n/a') + '</td>' +
    '<td>' + r.total_tokens + '</td>' +
    '<td>' + r.turn_count + '</td>' +
    '<td>' + Math.round(r.runtime_seconds) + 's</td>' +
  '</tr>').join('');
}
function renderRetrying(items) {
  const tbody = document.getElementById('retrying');
  if (items.length === 0) { tbody.innerHTML = '<tr><td colspan="4" class="meta">No retries queued</td></tr>'; return; }
  tbody.innerHTML = items.map(r => '<tr>' +
    '<td>' + (r.identifier || r.issue_id) + '</td>' +
    '<td class="status-retrying">#' + r.attempt + '</td>' +
    '<td>' + Math.round(r.due_in_ms/1000) + 's</td>' +
    '<td class="meta">' + (r.error || '') + '</td>' +
  '</tr>').join('');
}
function renderTotals(t) {
  if (!t) return;
  document.getElementById('totals').textContent =
    'Tokens: ' + t.TotalTokens + ' (in: ' + t.InputTokens + ', out: ' + t.OutputTokens + ')' +
    ' | Runtime: ' + Math.round(t.SecondsRunning) + 's';
}
setInterval(refresh, 1000);
refresh();
</script>
</body>
</html>`

func (s *Server) handleDashboard(c echo.Context) error {
	return c.HTML(http.StatusOK, dashboardHTML)
}
