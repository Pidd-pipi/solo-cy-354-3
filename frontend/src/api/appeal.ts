import request from '../utils/request'
import type { ReviewAppeal } from '../types'

type Envelope<T> = { code: number; message: string; data: T }

// 学生：对收到的某条评价提交申诉（每条评价只能申诉一次，仅评价接收方可提交）
export function createAppeal(data: { review_id: number; reason: string }) {
  return request.post<never, Envelope<ReviewAppeal>>('/appeals', data)
}

// 学生：查询我的全部申诉及处理进度
export function listMyAppeals() {
  return request.get<never, Envelope<ReviewAppeal[]>>('/appeals/me')
}

// 学生：查询单条申诉进度（仅发起人可查）
export function getAppeal(id: number) {
  return request.get<never, Envelope<ReviewAppeal>>(`/appeals/${id}`)
}

// 管理员：申诉列表，可按状态筛选 ?status=pending
export function adminListAppeals(status?: string) {
  return request.get<never, Envelope<ReviewAppeal[]>>('/admin/appeals', {
    params: status ? { status } : {},
  })
}

// 管理员：审核申诉（approve=通过并回滚信誉分，reject=驳回且分数不变）
export function adminReviewAppeal(id: number, data: { action: string; comment?: string }) {
  return request.post<never, Envelope<ReviewAppeal>>(`/admin/appeals/${id}/review`, data)
}
