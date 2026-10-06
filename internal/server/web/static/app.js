// Charts for agent pages: <div class="chart" data-src="/agents/1/metrics.json"></div>
(function () {
  function css(name) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  }

  function drawChart(el) {
    if (el.dataset.drawn) return;
    el.dataset.drawn = "1";
    fetch(el.dataset.src, { credentials: "same-origin" })
      .then(function (r) { return r.json(); })
      .then(function (d) {
        if (!d.t || d.t.length === 0) {
          el.innerHTML = '<p class="empty">No metrics yet.</p>';
          return;
        }
        var axis = { stroke: css("--muted"), grid: { stroke: css("--border") }, ticks: { stroke: css("--border") } };
        var opts = {
          width: el.clientWidth,
          height: 220,
          scales: { y: { range: [0, 100] } },
          axes: [axis, Object.assign({}, axis, { values: function (u, v) { return v.map(function (x) { return x + "%"; }); } })],
          series: [
            {},
            { label: "CPU", stroke: css("--chart-cpu"), width: 1.5, value: function (u, v) { return v == null ? "-" : v.toFixed(1) + "%"; } },
            { label: "Memory", stroke: css("--chart-mem"), width: 1.5, value: function (u, v) { return v == null ? "-" : v.toFixed(1) + "%"; } },
            { label: "Fullest disk", stroke: css("--chart-disk"), width: 1.5, value: function (u, v) { return v == null ? "-" : v.toFixed(1) + "%"; } }
          ]
        };
        var plot = new uPlot(opts, [d.t, d.cpu, d.memory, d.disk], el);
        new ResizeObserver(function () { plot.setSize({ width: el.clientWidth, height: 220 }); }).observe(el);
      })
      .catch(function () { el.innerHTML = '<p class="empty">Could not load metrics.</p>'; });
  }

  function init(root) {
    root.querySelectorAll(".chart[data-src]").forEach(drawChart);
  }

  document.addEventListener("DOMContentLoaded", function () { init(document); });
  document.addEventListener("htmx:afterSettle", function (e) { init(e.target); });

  // Confirmation dialogs: <form data-confirm="dialog-id"> opens the dialog first.
  document.addEventListener("click", function (e) {
    var open = e.target.closest("[data-open-dialog]");
    if (open) {
      document.getElementById(open.dataset.openDialog).showModal();
    }
    var close = e.target.closest("[data-close-dialog]");
    if (close) {
      close.closest("dialog").close();
    }
  });
})();
