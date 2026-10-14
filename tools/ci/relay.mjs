// Posts one message to the alert room through the relay, or says why it did not.
//
// ALERT_RELAY_URL and ALERT_RELAY_TOKEN come from the workflow; the token is read from SecretHive
// by relay-token.sh with the job's own forge token and is never stored. status is the relay's:
// "firing" for something to act on, "notice" for something to know.

export async function post(status, title, message) {
  const url = process.env.ALERT_RELAY_URL, token = process.env.ALERT_RELAY_TOKEN;
  if (!url || !token) { console.log('No alert relay configured: reported here only.'); return; }
  const response = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
    body: JSON.stringify({ status, title, message }),
  });
  if (!response.ok) throw new Error(`Alert relay answered HTTP ${response.status}`);
  console.log('Sent to the alert room.');
}
