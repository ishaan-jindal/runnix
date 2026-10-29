# Demo runbook — https://demo.runnix.xyz

```fish
# log in as the demo user
set TOKEN (curl -s -X POST https://demo.runnix.xyz/auth/login -d '{"username":"demo","password":"DemoPass123"}' | jq -r .accessToken)

# grab our tenant id
set TENANT (curl -s https://demo.runnix.xyz/users/me -H "Authorization: Bearer $TOKEN" | jq -r '.tenants[0].id')

# what can it run? (7 languages on v0.2.0)
curl -s https://demo.runnix.xyz/languages | jq .

# submit python, keep the id
set ID (curl -s -X POST https://demo.runnix.xyz/executions -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: $TENANT" -d '{"language":"python","source":"print(40+2)"}' | jq -r .id)

# poll the result
curl -s https://demo.runnix.xyz/executions/$ID -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: $TENANT" | jq '{status, stdout, exit_code}'

# same in javascript and other languages
set JID (curl -s -X POST https://demo.runnix.xyz/executions -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: $TENANT" -d '{"language":"javascript","source":"console.log(40+2)"}' | jq -r .id)
sleep 5; curl -s https://demo.runnix.xyz/executions/$JID -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: $TENANT" | jq '{status, stdout}'

# compiled language needs a real timeout (default 2s won't survive a JDK launch)
set KID (curl -s -X POST https://demo.runnix.xyz/executions -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: $TENANT" -d '{"language":"java","source":"public class Main { public static void main(String[] a) { System.out.println(\"Hello from Java!\"); } }","timeout_s":30}' | jq -r .id)
sleep 12; curl -s https://demo.runnix.xyz/executions/$KID -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: $TENANT" | jq '{status, stdout}'

# quotas bite: free tier allows 2 concurrent runs, so occupy both...
curl -s -X POST https://demo.runnix.xyz/executions -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: $TENANT" -d '{"language":"python","source":"import time; time.sleep(50)","timeout_s":55}' &
curl -s -X POST https://demo.runnix.xyz/executions -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: $TENANT" -d '{"language":"python","source":"import time; time.sleep(50)","timeout_s":55}' &
sleep 3
# ...and watch the third get 429 + Retry-After
curl -s -o /dev/null -w "%{http_code}\n" -D - -X POST https://demo.runnix.xyz/executions -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: $TENANT" -d '{"language":"python","source":"print(1)"}' | grep -i -E "^HTTP|retry-after"

# signed webhook: submit with a https://webhook.site/<uuid> URL, refresh the bin, check X-Runnix-Signature
curl -s -X POST https://demo.runnix.xyz/executions -H "Authorization: Bearer $TOKEN" -H "X-Tenant-ID: $TENANT" -d '{"language":"python","source":"print(40+2)","webhook_url":"https://webhook.site/<your-uuid>"}' | jq '{id, status}'
```

