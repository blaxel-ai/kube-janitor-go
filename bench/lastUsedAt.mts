import { SandboxInstance } from "@blaxel/core"

const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms))

const sandbox = await SandboxInstance.create({
  lifecycle: {
    expirationPolicies: [
      {
        type: "ttl-idle",
        value: "30s",  // Delete after 30 seconds of inactivity
        action: "delete"
      }
    ]
  }
})

const sandboxName = sandbox.metadata?.name
console.log(`Created sandbox: ${sandboxName}`)
console.log("Starting activity for 20 seconds (calling every 1s)...")

// Keep sandbox active for 20 seconds
for (let i = 0; i < 20; i++) {
  await sandbox.process.exec({
    command: "echo 'Hello, world!'",
    waitForCompletion: true,
  })
  console.log(`Activity ${i + 1}/20`)
  await sleep(1000)
}

console.log("Activity stopped. Checking sandbox is still running...")

// Check sandbox is still running right after activity stops
let sbxCheck = await SandboxInstance.get(sandboxName!)
let statusCheck = sbxCheck.status

if (statusCheck === "TERMINATED" || statusCheck === "Terminated") {
  console.error(`FAILURE: Sandbox terminated too early (right after activity). Status: ${statusCheck}`)
  process.exit(1)
}
console.log(`Status after activity: ${statusCheck} (expected: running)`)

// Wait 20s (still within 30s idle timeout)
console.log("Waiting 20s (still within idle timeout)...")
await sleep(20 * 1000)

sbxCheck = await SandboxInstance.get(sandboxName!)
statusCheck = sbxCheck.status

if (statusCheck === "TERMINATED" || statusCheck === "Terminated") {
  console.error(`FAILURE: Sandbox terminated too early (at 20s idle). Status: ${statusCheck}`)
  process.exit(1)
}
console.log(`Status at 20s idle: ${statusCheck} (expected: still running)`)

// Wait another 20s (30s idle timeout + 10s margin)
console.log("Waiting another 20s for idle timeout (30s) + margin (10s)...")
await sleep(20 * 1000)

// Check if sandbox is now terminated
const sbx = await SandboxInstance.get(sandboxName!)
const status = sbx.status

console.log(`Sandbox status: ${status}`)

if (status === "TERMINATED" || status === "Terminated") {
  console.log("SUCCESS: Sandbox was terminated by idle timeout as expected")
} else {
  console.error(`FAILURE: Expected sandbox to be terminated, but got: ${status}`)
  process.exit(1)
}
