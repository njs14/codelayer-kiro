import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import type { KiroMetadata } from '@/lib/daemon/types'
import { Coins, Gauge } from 'lucide-react'

interface KiroMetadataBadgeProps {
  metadata?: KiroMetadata | null
}

export function KiroMetadataBadge({ metadata }: KiroMetadataBadgeProps) {
  if (!metadata) return null

  const contextPct = metadata.contextUsagePercentage
  const credits = metadata.credits

  // Color coding for context usage
  const getContextColor = (pct: number) => {
    if (pct >= 90) return 'text-destructive'
    if (pct >= 70) return 'text-yellow-500'
    return 'text-muted-foreground'
  }

  return (
    <div className="flex items-center gap-2">
      {/* Credits consumed */}
      <TooltipProvider>
        <Tooltip>
          <TooltipTrigger asChild>
            <span className="inline-flex items-center gap-1 text-xs font-mono text-muted-foreground">
              <Coins className="h-3 w-3" />
              {credits.toFixed(2)}
            </span>
          </TooltipTrigger>
          <TooltipContent>Kiro credits consumed this session</TooltipContent>
        </Tooltip>
      </TooltipProvider>

      {/* Context window usage */}
      <TooltipProvider>
        <Tooltip>
          <TooltipTrigger asChild>
            <span
              className={`inline-flex items-center gap-1 text-xs font-mono ${getContextColor(contextPct)}`}
            >
              <Gauge className="h-3 w-3" />
              {contextPct.toFixed(0)}%
            </span>
          </TooltipTrigger>
          <TooltipContent>Kiro context window usage: {contextPct.toFixed(1)}%</TooltipContent>
        </Tooltip>
      </TooltipProvider>
    </div>
  )
}
