export const APPEAL_STATUSES = [
  { value: 'pending', label: '待审核', type: 'warning' },
  { value: 'approved', label: '已通过', type: 'success' },
  { value: 'rejected', label: '已驳回', type: 'danger' },
] as const

export const APPEAL_ACTIONS = [
  { value: 'approve', label: '审核通过（撤销信誉分变化）' },
  { value: 'reject', label: '驳回（信誉分不变）' },
] as const

export function appealStatusLabel(value: string): string {
  return APPEAL_STATUSES.find((s) => s.value === value)?.label ?? value
}

export function appealStatusType(value: string): string {
  return APPEAL_STATUSES.find((s) => s.value === value)?.type ?? 'info'
}
