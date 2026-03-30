import { connectWithRetry } from '../daemonClient.js'
import { resolveFullConfig } from '../config.js'
import { homedir } from 'os'
import { join } from 'path'

export type Provider = 'claude' | 'kiro'

// Kiro-supported models with credit multiplier info
export const KIRO_MODELS = [
  { value: 'auto', label: 'Auto (1.0x credits)', multiplier: 1.0 },
  { value: 'claude-opus4.6', label: 'Claude Opus 4.6 (2.2x credits)', multiplier: 2.2 },
  { value: 'claude-sonnet4.6', label: 'Claude Sonnet 4.6 (1.3x credits)', multiplier: 1.3 },
  { value: 'claude-sonnet4.5', label: 'Claude Sonnet 4.5 (1.3x credits)', multiplier: 1.3 },
  { value: 'minimax-2.5', label: 'MiniMax 2.5 (0.25x credits)', multiplier: 0.25 },
  { value: 'minimax-2.1', label: 'MiniMax 2.1 (0.25x credits)', multiplier: 0.25 },
  { value: 'qwen3-coder-next', label: 'Qwen3 Coder Next (0.05x credits)', multiplier: 0.05 },
  { value: 'deepseek-3.2', label: 'DeepSeek 3.2 (0.05x credits)', multiplier: 0.05 },
] as const

// Claude model shortcuts for the direct Claude provider
export const CLAUDE_MODELS = [
  { value: 'sonnet', label: 'Sonnet (balanced)' },
  { value: 'opus', label: 'Opus (most capable)' },
  { value: 'haiku', label: 'Haiku (fastest)' },
] as const

interface LaunchOptions {
  query?: string
  provider?: Provider
  title?: string
  model?: string
  workingDir?: string
  additionalDirectories?: string[]
  addDir?: string[] // CLI option name maps to this
  maxTurns?: number
  daemonSocket?: string
  configFile?: string
  approvals?: boolean
  dangerouslySkipPermissions?: boolean
  dangerouslySkipPermissionsTimeout?: string
}

export const launchCommand = async (query: string, options: LaunchOptions = {}) => {
  try {
    const provider: Provider = options.provider || 'claude'

    // Get socket path from configuration
    const config = resolveFullConfig(options)
    let socketPath = config.daemon_socket

    // Expand ~ to home directory if needed
    if (socketPath.startsWith('~')) {
      socketPath = join(homedir(), socketPath.slice(1))
    }

    // Handle additional directories from either option name
    const additionalDirs = options.additionalDirectories || options.addDir || []

    const providerLabel = provider === 'kiro' ? 'Kiro' : 'Claude Code'
    console.log(`Launching ${providerLabel} session...`)
    console.log('Query:', query)
    if (options.title) console.log('Title:', options.title)
    if (options.model) console.log('Model:', options.model)
    console.log('Working directory:', options.workingDir || process.cwd())
    if (additionalDirs.length > 0) {
      console.log('Additional directories:', additionalDirs)
    }
    console.log('Daemon socket:', socketPath)

    if (provider === 'kiro') {
      // Kiro uses native ACP permissions — no MCP approval server needed
      console.log('Provider: Kiro (ACP native permissions)')
    } else {
      console.log('Approvals enabled:', options.approvals !== false)
    }

    if (options.dangerouslySkipPermissions) {
      console.log('⚠️  Dangerously skip permissions enabled - ALL tools will be auto-approved')
      if (options.dangerouslySkipPermissionsTimeout) {
        console.log(`   Auto-disabling after ${options.dangerouslySkipPermissionsTimeout} minutes`)
      }
    }

    // Connect to daemon
    const client = await connectWithRetry(socketPath, 3, 1000)

    try {
      // For Kiro, skip MCP approval server — ACP handles permissions natively
      const permissionPromptTool =
        provider === 'kiro'
          ? undefined
          : options.approvals !== false
            ? 'mcp__codelayer__request_permission'
            : undefined

      // Launch the session
      const result = await client.launchSession({
        query: query,
        title: options.title,
        model: options.model,
        provider: provider,
        working_dir: options.workingDir || process.cwd(),
        additional_directories: additionalDirs,
        max_turns: options.maxTurns,
        permission_prompt_tool: permissionPromptTool,
        dangerously_skip_permissions: options.dangerouslySkipPermissions,
        dangerously_skip_permissions_timeout: options.dangerouslySkipPermissionsTimeout
          ? parseInt(options.dangerouslySkipPermissionsTimeout) * 60 * 1000
          : undefined,
      })

      console.log('\nSession launched successfully!')
      console.log('Session ID:', result.session_id)
      console.log('Run ID:', result.run_id)
      console.log('\nYou can now use CodeLayer to manage this session.')
    } finally {
      // Close the client connection
      client.close()
    }
  } catch (error) {
    console.error('Failed to launch session:', error)
    console.error('\nMake sure the daemon is running. You can start it with:')
    console.error('  npx humanlayer tui')
    process.exit(1)
  }
}
