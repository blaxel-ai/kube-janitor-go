import { SandboxInstance } from "@blaxel/core"
import { parseArgs } from "node:util"

interface Args {
  total: number
  batchSize: number
  ttl: string
  memory: number
  prefix: string
}

function parseCliArgs(): Args {
  const { values } = parseArgs({
    options: {
      total: {
        type: "string",
        short: "n",
        default: "10",
      },
      "batch-size": {
        type: "string",
        short: "j",
        default: "5",
      },
      ttl: {
        type: "string",
        short: "t",
        default: "30s",
      },
      memory: {
        type: "string",
        short: "m",
        default: "2048",
      },
      prefix: {
        type: "string",
        short: "p",
        default: "bench-janitor",
      },
    },
  })

  return {
    total: parseInt(values.total ?? "10", 10),
    batchSize: parseInt(values["batch-size"] ?? "5", 10),
    ttl: values.ttl ?? "5m",
    memory: parseInt(values.memory ?? "512", 10),
    prefix: values.prefix ?? "bench-sbx",
  }
}

async function createSandbox(name: string, ttl: string, memory: number): Promise<SandboxInstance> {
  const sandbox = await SandboxInstance.create({
    name,
    memory,
    ttl,
  })
  return sandbox
}

async function createBatch(
  startIndex: number,
  batchSize: number,
  args: Args
): Promise<{ success: number; failed: number }> {
  const promises: Promise<SandboxInstance | null>[] = []

  for (let i = 0; i < batchSize; i++) {
    const index = startIndex + i
    const name = `${args.prefix}-${index}`
    promises.push(
      createSandbox(name, args.ttl, args.memory)
        .then((sbx) => {
          console.log(`Created sandbox: ${name}`)
          return sbx
        })
        .catch((err) => {
          console.error(`Failed to create sandbox ${name}:`, err.message)
          return null
        })
    )
  }

  const results = await Promise.all(promises)
  const success = results.filter((r) => r !== null).length
  const failed = results.filter((r) => r === null).length

  return { success, failed }
}

async function main() {
  const args = parseCliArgs()

  console.log(`Creating ${args.total} sandboxes in batches of ${args.batchSize}`)
  console.log(`TTL: ${args.ttl}, Memory: ${args.memory}MB, Prefix: ${args.prefix}`)
  console.log("---")

  const startTime = Date.now()
  let totalSuccess = 0
  let totalFailed = 0

  for (let i = 0; i < args.total; i += args.batchSize) {
    const batchNumber = Math.floor(i / args.batchSize) + 1
    const remaining = args.total - i
    const currentBatchSize = Math.min(args.batchSize, remaining)

    console.log(`\nBatch ${batchNumber}: Creating ${currentBatchSize} sandboxes...`)
    const batchStart = Date.now()

    const { success, failed } = await createBatch(i, currentBatchSize, args)

    const batchDuration = ((Date.now() - batchStart) / 1000).toFixed(2)
    console.log(`Batch ${batchNumber} completed in ${batchDuration}s (success: ${success}, failed: ${failed})`)

    totalSuccess += success
    totalFailed += failed
  }

  const totalDuration = ((Date.now() - startTime) / 1000).toFixed(2)
  console.log("\n---")
  console.log(`Completed in ${totalDuration}s`)
  console.log(`Total created: ${totalSuccess}`)
  console.log(`Total failed: ${totalFailed}`)
}

main().catch(console.error)
