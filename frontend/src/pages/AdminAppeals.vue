<template>
  <div class="page">
    <h2>信誉申诉审核（管理员）</h2>
    <el-card>
      <template #header>
        <div class="head">
          <el-radio-group v-model="statusFilter" @change="load">
            <el-radio-button label="">全部</el-radio-button>
            <el-radio-button label="pending">待审核</el-radio-button>
            <el-radio-button label="approved">已通过</el-radio-button>
            <el-radio-button label="rejected">已驳回</el-radio-button>
          </el-radio-group>
          <el-button size="small" @click="load">刷新</el-button>
        </div>
      </template>
      <el-table :data="appeals" v-loading="loading">
        <el-table-column prop="id" label="申诉ID" width="80" />
        <el-table-column label="申诉人" width="120">
          <template #default="{ row }">{{ row.appellant_name }} (#{{ row.appellant_id }})</template>
        </el-table-column>
        <el-table-column label="原评价" width="170">
          <template #default="{ row }">
            <div v-if="row.review">
              <el-tag size="small">{{ ratingLabel(row.review.rating) }}</el-tag>
              <span class="muted"> 评价 #{{ row.review.id }}</span>
              <div class="muted">评价人 #{{ row.review.reviewer_id }}</div>
            </div>
            <span v-else class="muted">#{{ row.review_id }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="reason" label="申诉理由" show-overflow-tooltip />
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="appealStatusType(row.status)">{{ appealStatusLabel(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="信誉分处理" width="140">
          <template #default="{ row }">
            <el-tag v-if="row.status === 'approved'" size="small" type="success">
              已撤销 {{ row.credit_delta > 0 ? '+' + row.credit_delta : row.credit_delta }}
            </el-tag>
            <el-tag v-else-if="row.status === 'rejected'" size="small" type="info">分数不变</el-tag>
            <span v-else class="muted">待处理</span>
          </template>
        </el-table-column>
        <el-table-column label="提交时间" width="160">
          <template #default="{ row }">{{ formatDateTime(row.created_at) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="190" fixed="right">
          <template #default="{ row }">
            <template v-if="row.status === 'pending'">
              <el-button size="small" type="success" @click="openReview(row, 'approve')">通过</el-button>
              <el-button size="small" type="danger" @click="openReview(row, 'reject')">驳回</el-button>
            </template>
            <span v-else class="muted">
              {{ row.review_comment || '已处理' }}
            </span>
          </template>
        </el-table-column>
      </el-table>
      <el-empty v-if="!loading && appeals.length === 0" description="暂无申诉" />
    </el-card>

    <el-dialog v-model="reviewVisible" :title="action === 'approve' ? '审核通过' : '驳回申诉'" width="460px">
      <el-alert
        v-if="action === 'approve'"
        title="通过后将撤销该评价带来的信誉分变化（好评 +5 扣回 / 差评 -10 补回 / 中评无变化）。"
        type="success"
        :closable="false"
        show-icon
        class="tip"
      />
      <el-alert
        v-else
        title="驳回后原评价继续生效，申诉人的信誉分保持不变。每条评价只能申诉一次，驳回不可再次申诉。"
        type="warning"
        :closable="false"
        show-icon
        class="tip"
      />
      <el-form label-width="80px">
        <el-form-item label="审核备注">
          <el-input v-model="comment" type="textarea" :rows="3" maxlength="500" show-word-limit placeholder="可选，填写处理依据" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="reviewVisible = false">取消</el-button>
        <el-button :type="action === 'approve' ? 'success' : 'danger'" :loading="submitting" @click="submit">确认</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { adminListAppeals, adminReviewAppeal } from '../api/appeal'
import { ratingLabel } from '../constants/trade'
import { appealStatusLabel, appealStatusType } from '../constants/appeal'
import { formatDateTime } from '../utils/dateFormat'
import type { ReviewAppeal } from '../types'

const appeals = ref<ReviewAppeal[]>([])
const loading = ref(false)
const statusFilter = ref('pending')
const reviewVisible = ref(false)
const submitting = ref(false)
const current = ref<ReviewAppeal | null>(null)
const action = ref<'approve' | 'reject'>('approve')
const comment = ref('')

async function load() {
  loading.value = true
  try {
    const res = await adminListAppeals(statusFilter.value || undefined)
    appeals.value = res.data
  } finally {
    loading.value = false
  }
}

function openReview(row: ReviewAppeal, act: 'approve' | 'reject') {
  current.value = row
  action.value = act
  comment.value = ''
  reviewVisible.value = true
}

async function submit() {
  if (!current.value) return
  submitting.value = true
  try {
    const res = await adminReviewAppeal(current.value.id, { action: action.value, comment: comment.value.trim() })
    if (action.value === 'approve') {
      ElMessage.success(`已通过，信誉分调整 ${res.data.credit_delta > 0 ? '+' + res.data.credit_delta : res.data.credit_delta}`)
    } else {
      ElMessage.success('已驳回，信誉分不变')
    }
    reviewVisible.value = false
    await load()
  } finally {
    submitting.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.head {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.muted {
  color: #909399;
  font-size: 12px;
}
.tip {
  margin-bottom: 12px;
}
</style>
