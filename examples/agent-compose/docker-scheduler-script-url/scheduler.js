scheduler.cron("daily-review", "0 9 * * *", function dailyReview() {
  return scheduler.agent("Review the current project state.");
});

scheduler.timeout("source-loaded", function sourceLoaded() {
  return scheduler.shell("printf 'scheduler script URL ok\\n'");
}, 2000);

function main(payload) {
  return { ok: true, payload };
}
