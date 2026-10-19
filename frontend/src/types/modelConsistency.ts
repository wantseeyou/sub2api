export type ModelConsistencyStatus =
  | 'running'
  | 'matched'
  | 'suspected_mismatch'
  | 'inconclusive'
  | 'failed'
  | 'cancelled'
  | 'expired'

export interface ModelConsistencyResult {
  // 本次检测批次标识。
  run_id: string
  // 管理员选择的请求模型。
  requested_model: string
  // 账号映射后的实际上游模型。
  upstream_model: string
  // 检测状态及经验判定。
  status: ModelConsistencyStatus
  // 服务端提供的稳定原因代码。
  reason: string
  // 检测开始时间。
  started_at: string
  // 本批次执行截止时间。
  deadline_at: string
  // 已结束检测的完成时间。
  completed_at?: string
  // 结果是否仍适用于账号当前配置。
  applicable: boolean
  // 实际采样次数。
  sample_count: number
  // 有效且完整的采样次数。
  valid_sample_count: number
  // 参考指纹中的候选模型，仅用于诊断。
  candidate_model?: string
  // 分类器原始分数，不代表准确率或能力。
  score?: number
  // 检测实际使用的上游传输协议。
  transport: 'http' | 'websocket'
  // 传输选择或回退限制的诊断代码。
  transport_reason?: string
  // 固定请求参数组合。
  profile: string
  // 指纹、算法、挑战和请求参数版本。
  versions: {
    // 内置指纹库版本。
    bank: string
    // 分类算法版本。
    algorithm: string
    // 挑战生成版本。
    challenge: string
    // 请求参数版本。
    profile: string
  }
  // 上游报告的实际令牌消耗，缺省表示未提供。
  usage?: {
    // 输入令牌数。
    input_tokens: number
    // 输出令牌数。
    output_tokens: number
  }
  // 本次经验判断已知的可比性或准确率限制。
  limitation?: string
}

export interface ModelConsistencySnapshot {
  // 账号当前是否支持独立检测。
  supported: boolean
  // 不支持检测时的原因代码。
  unsupported_reason?: string
  // 内置参考指纹覆盖的上游模型。
  reference_models: string[]
  // 每个请求模型最近启动的一次结果。
  results: ModelConsistencyResult[]
}

export interface ModelConsistencyRun {
  // 新创建的检测批次标识。
  run_id: string
  // 本批次各模型的初始结果。
  results: ModelConsistencyResult[]
}
