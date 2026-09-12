<template>
  <div class="page">
    <h2>信誉申诉</h2>

    <el-card class="section">
      <template #header>⭐ 我收到的评价</template>
      <el-table :data="reviews" v-loading="loadingReviews">
        <el-table-column prop="id" label="评价ID" width="90" />
        <el-table-column label="评分" width="90">
          <template #default="{ row }">
            <el-tag size="small">{{ ratingLabel(row.rating) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="content" label="评价内容" show-overflow-tooltip />
        <el-table-column label="时间" width="160">
          <template #default="{ row }">{{ formatDateTime(row.created_at) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="170">
          <template #default="{ row }">
            <el-button
              v-if="!appealByReview[row.id]"
              size="small"
              type="warning"
              @click="openAppeal(row)"
            >申诉</el-button>
            <template v-else>
              <el-tag size="small" :type="appealStatusType(appealByReview[row.id].status)">
                {{ appealStatusLabel(appealByReview[row.id].status) }}
              </el-tag>
              <el-button link type="primary" size="small" @click="scrollToProgress">查看进度</el-button>
            </template>
          </template>
        </el-table-column>
      </el-table>
      <el-empty v-if="!loadingReviews && reviews.length === 0" description="暂无收到的评价" />
    </el-card>

    <el-card ref="progressCard" class="section">
      <template #header>
        <div class="progress-head">
          <span>📨 我的申诉进度</span>
          <el-button size="small" @click="loadAppeals">刷新进度</el-button>
        </div>
      </template>
      <el-table :data="appeals" v-loading="loadingAppeals">
        <el-table-column prop="id" label="申诉ID" width="90" />
        <el-table-column label="原评价" width="100">
          <template #default="{ row }">
            <el-tag size="small">#{{ row.review_id }} {{ row.review ? ratingLabel(row.review.rating) : '' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="reason" label="申诉理由" show-overflow-tooltip />
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="appealStatusType(row.status)">{{ appealStatusLabel(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="信誉分处理" width="150">
          <template #default="{ row }">
            <span v-if="row.status === 'approved'">
              <el-tag size="small" type="success">已撤销 {{ formatDelta(row.credit_delta) }}</el-tag>
            </span>
            <span v-else-if="row.status === 'rejected'">
              <el-tag size="small" type="info">分数不变</el-tag>
            </span>
            <span v-else class="muted">等待审核</span>
          </template>
        </el-table-column>
        <el-table-column prop="review_comment" label="审核备注" show-overflow-tooltip>
          <template #default="{ row }">{{ row.review_comment || '—' }}</template>
        </el-table-column>
        <el-table-column label="审核时间" width="160">
          <template #default="{ row }">{{ row.reviewed_at ? formatDateTime(row.reviewed_at) : '—' }}</template>
        </el-table-column>
      </el-table>
      <el-empty v-if="!loadingAppeals && appeals.length === 0" description="暂未提交过申诉" />
    </el-card>

    <AppealDialog ref="appealDialogRef" :review="activeReview" @submitted="onAppealSubmitted" />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import AppealDialog from '../components/common/AppealDialog.vue'
import { listMyReviews } from '../api/review'
import { listMyAppeals } from '../api/appeal'
import { ratingLabel } from '../constants/trade'
import { appealStatusLabel, appealStatusType } from '../constants/appeal'
import { formatDateTime } from '../utils/dateFormat'
import { useAuthStore } from '../stores/authStore'
import { getProfile } from '../api/user'
import type { Review, ReviewAppeal } from '../types'

const authStore = useAuthStore()
const reviews = ref<Review[]>([])
const appeals = ref<ReviewAppeal[]>([])
const loadingReviews = ref(false)
const loadingAppeals = ref(false)
const activeReview = ref<Review | null>(null)
const appealDialogRef = ref<InstanceType<typeof AppealDialog> | null>(null)
const progressCard = ref()

const appealByReview = computed<Record<number, ReviewAppeal>>(() => {
  const map: Record<number, ReviewAppeal> = {}
  for (const a of appeals.value) map[a.review_id] = a
  return map
})

function formatDelta(delta: number): string {
  if (delta > 0) return `+${delta}`
  return String(delta)
}

function openAppeal(row: Review) {
  activeReview.value = row
  appealDialogRef.value?.open()
}

async function onAppealSubmitted() {
  ElMessage.success('申诉提交成功')
  await loadAppeals()
}

function scrollToProgress() {
  progressCard.value?.$el?.scrollIntoView({ behavior: 'smooth' })
}

async function loadReviews() {
  loadingReviews.value = true
  try {
    const res = await listMyReviews()
    reviews.value = res.data
  } finally {
    loadingReviews.value = false
  }
}

async function loadAppeals() {
  loadingAppeals.value = true
  try {
    const res = await listMyAppeals()
    appeals.value = res.data
  } finally {
    loadingAppeals.value = false
  }
}

onMounted(async () => {
  await Promise.all([loadReviews(), loadAppeals()])
  // 审核结果可能已改变信誉分，刷新本地用户信息
  const profile = await getProfile()
  authStore.user = profile.data
})
</script>

<style scoped>
.section {
  margin-bottom: 16px;
}
.progress-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.muted {
  color: #909399;
  font-size: 12px;
}
</style>
