import { post } from '@/utils/request'

// RescanReport mirrors the backend internal/plugin.RescanReport.
export interface RescanReport {
  added: string[]
  skipped: string[]
  changed: string[]
  errors?: string[]
}

export function rescanPlugins(): Promise<RescanReport> {
  return post('/api/v1/plugins/rescan', {}).then((res: any) => {
    if (res && typeof res === 'object' && 'success' in res) {
      return (res.data as RescanReport) || { added: [], skipped: [], changed: [] }
    }
    return res as RescanReport
  })
}
