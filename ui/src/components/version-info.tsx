import { useVersionInfo } from '@/lib/api'

export function VersionInfo() {
  const { data: versionInfo } = useVersionInfo()

  if (!versionInfo) return null

  const handleCommitClick = (e: React.MouseEvent) => {
    e.preventDefault()
    e.stopPropagation()
    // commitUrl is built server-side from the build's CommitURLBase (upstream GitHub by
    // default, or the fork repo for fork builds), so it points at the repo the commit
    // actually lives on instead of a hardcoded upstream URL.
    if (versionInfo.commitUrl) {
      window.open(versionInfo.commitUrl, '_blank')
    }
  }

  return (
    <div className="text-[10px] text-muted-foreground/60 font-mono leading-none">
      v{versionInfo.version.replace(/^v/, '')} •{' '}
      <button
        onClick={handleCommitClick}
        className="hover:text-primary/80 hover:underline transition-colors cursor-pointer"
        title={`View commit ${versionInfo.commitId}`}
      >
        {versionInfo.commitId.slice(0, 7)}
      </button>
    </div>
  )
}
