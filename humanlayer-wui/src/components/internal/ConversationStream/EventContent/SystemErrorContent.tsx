import { AlertCircle } from 'lucide-react'

export function SystemErrorContent({ content }: { content: string }) {
  // Strip the "Error: " prefix if present for cleaner display
  const errorMessage = content.startsWith('Error: ') ? content.slice(7) : content

  return (
    <div className="flex items-start gap-2 text-sm">
      <AlertCircle className="w-4 h-4 text-destructive flex-shrink-0 mt-0.5" />
      <span className="text-destructive">{errorMessage}</span>
    </div>
  )
}
