import { SandboxInstance } from "@blaxel/core"

const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms))

async function testTtlRecreate() {
  console.log("=== Test 1: TTL expiration and recreate ===")

  const sandboxName = `test-ttl-recreate-${Date.now()}`

  // Create sandbox with 5s TTL
  console.log(`Creating sandbox "${sandboxName}" with 5s TTL...`)
  const sandbox1 = await SandboxInstance.create({
    name: sandboxName,
    ttl: "5s",
  })
  console.log(`Created sandbox: ${sandbox1.metadata?.name}`)

  // Wait 6 seconds for TTL to expire
  console.log("Waiting 5.1s for TTL to expire...")
  await sleep(5100)

  // Recreate sandbox with same name
  console.log(`Recreating sandbox "${sandboxName}" with same name...`)
  const sandbox2 = await SandboxInstance.create({
    name: sandboxName,
    ttl: "30s",
  })
  console.log(`Recreated sandbox: ${sandbox2.metadata?.name}`)

  // Verify it's running
  const sbx2Check = await SandboxInstance.get(sandboxName)
  const status2 = sbx2Check.status

  if (status2 === "TERMINATED" || status2 === "Terminated") {
    console.error(`FAILURE: Recreated sandbox should be running, but got: ${status2}`)
    process.exit(1)
  }

  console.log(`Recreated sandbox status: ${status2}`)
  console.log("SUCCESS: TTL recreate test passed!\n")
}

async function testIdleTtlRecreate() {
  console.log("=== Test 2: Idle-TTL expiration and recreate ===")

  const sandboxName = `test-idle-ttl-recreate-${Date.now()}`

  // Create sandbox with 5s idle TTL
  console.log(`Creating sandbox "${sandboxName}" with 5s idle TTL...`)
  const sandbox1 = await SandboxInstance.create({
    name: sandboxName,
    lifecycle: {
      expirationPolicies: [
        {
          type: "ttl-idle",
          value: "5s",
          action: "delete"
        }
      ]
    }
  })
  console.log(`Created sandbox: ${sandbox1.metadata?.name}`)

  // Wait 6 seconds for idle TTL to expire (no activity)
  console.log("Waiting 6s for idle TTL to expire (no activity)...")
  await sleep(5100)

  // Recreate sandbox with same name
  console.log(`Recreating sandbox "${sandboxName}" with same name...`)
  const sandbox2 = await SandboxInstance.create({
    name: sandboxName,
    lifecycle: {
      expirationPolicies: [
        {
          type: "ttl-idle",
          value: "30s",
          action: "delete"
        }
      ]
    }
  })
  console.log(`Recreated sandbox: ${sandbox2.metadata?.name}`)

  // Verify it's running
  const sbx2Check = await SandboxInstance.get(sandboxName)
  const status2 = sbx2Check.status

  if (status2 === "TERMINATED" || status2 === "Terminated") {
    console.error(`FAILURE: Recreated sandbox should be running, but got: ${status2}`)
    process.exit(1)
  }

  console.log(`Recreated sandbox status: ${status2}`)
  console.log("SUCCESS: Idle-TTL recreate test passed!\n")
}

async function main() {
  console.log("Testing sandbox recreation after TTL expiration\n")

  await testTtlRecreate()
  await testIdleTtlRecreate()

  console.log("=== All tests passed! ===")
}

main().catch((err) => {
  console.error("Test failed:", err)
  process.exit(1)
})
