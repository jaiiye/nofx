import { ShieldAlert } from 'lucide-react'
import type { ProtectionConfig } from '../../types'

interface ProtectionEditorProps {
  config?: ProtectionConfig
  onChange: (config: ProtectionConfig) => void
  disabled?: boolean
  language: string
}

// Mirrors store.DefaultProtectionConfig() on the backend.
const DEFAULTS = {
  max_drawdown_pct: 15,
  max_drawdown_lookback_h: 168,
  drawdown_basis: 'equity' as const,
  losing_closes_guard_count: 3,
  losing_closes_guard_lookback_h: 6,
  cooldown_minutes: 60,
  low_profit_lookback_h: 168,
  low_profit_min_trades: 4,
}

const L = {
  title: { zh: '硬熔断保护', en: 'Circuit-breaker protections' },
  subtitle: {
    zh: '代码强制拦截，AI 无法绕过。任一条件触发即禁止开新仓，条件消失后自动恢复。',
    en: 'Code-enforced blocks the AI cannot bypass. Any trigger halts new opens; recovery is automatic once the condition clears.',
  },
  drawdown: { zh: '最大回撤熔断', en: 'Max drawdown halt' },
  drawdownDesc: {
    zh: '所选口径较窗口峰值回撤超过该百分比时暂停开仓',
    en: 'Pause opens when the selected curve is more than this % below its window peak',
  },
  maxDrawdown: { zh: '回撤阈值 (%)', en: 'Drawdown limit (%)' },
  lookback: { zh: '回看窗口 (小时)', en: 'Lookback (hours)' },
  basis: { zh: '回撤口径', en: 'Drawdown basis' },
  basisEquity: { zh: '权益 (含未实现盈亏)', en: 'Equity (incl. unrealized)' },
  basisBalance: { zh: '余额 (仅已实现)', en: 'Balance (realized only)' },
  losing: { zh: '连续亏损熔断', en: 'Losing-streak guard' },
  losingDesc: {
    zh: '回看窗口内亏损平仓达到该次数时暂停开仓',
    en: 'Pause opens after this many losing closes within the lookback window',
  },
  losingCount: { zh: '亏损次数阈值', en: 'Losing closes' },
  cooldown: { zh: '同币冷却', en: 'Per-symbol cooldown' },
  cooldownDesc: {
    zh: '某币平仓后，该时长内禁止重开，防止立刻反手',
    en: 'Block re-opening a symbol for this long after it was closed',
  },
  cooldownMin: { zh: '冷却时长 (分钟)', en: 'Cooldown (minutes)' },
  lowProfit: { zh: '不盈利币拉黑', en: 'Low-profit pair block' },
  lowProfitDesc: {
    zh: '回看窗口内平仓次数达标且累计盈亏 ≤ 0 时拉黑该币',
    en: 'Drop a symbol once it has this many closes within the window with non-positive total PnL',
  },
  lowProfitMin: { zh: '最少平仓次数', en: 'Min closes' },
  disabledHint: { zh: '留空使用默认值', en: 'Leave blank to use the default' },
}

const t = (key: keyof typeof L, language: string) =>
  L[key][language === 'zh' ? 'zh' : 'en']

export function ProtectionEditor({
  config,
  onChange,
  disabled,
  language,
}: ProtectionEditorProps) {
  const c = config ?? {}

  const updateField = <K extends keyof ProtectionConfig>(
    key: K,
    value: ProtectionConfig[K]
  ) => {
    if (!disabled) {
      onChange({ ...c, [key]: value })
    }
  }

  const numField = (
    key: keyof ProtectionConfig,
    label: string,
    fallback: number,
    min: number,
    max: number,
    step: number
  ) => (
    <div
      className="p-4 rounded-lg"
      style={{ background: '#0B0E11', border: '1px solid #2B3139' }}
    >
      <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>
        {label}
      </label>
      <input
        type="number"
        value={(c[key] as number | undefined) ?? fallback}
        onChange={(e) =>
          updateField(key, (parseFloat(e.target.value) || fallback) as never)
        }
        disabled={disabled}
        min={min}
        max={max}
        step={step}
        className="w-28 px-3 py-2 rounded"
        style={{
          background: '#1E2329',
          border: '1px solid #2B3139',
          color: '#EAECEF',
        }}
      />
    </div>
  )

  return (
    <div className="space-y-6">
      <div>
        <div className="flex items-center gap-2 mb-2">
          <ShieldAlert className="w-5 h-5" style={{ color: '#F6465D' }} />
          <h3 className="font-medium" style={{ color: '#EAECEF' }}>
            {t('title', language)}
          </h3>
        </div>
        <p className="text-xs leading-5 mb-2" style={{ color: '#848E9C' }}>
          {t('subtitle', language)}
        </p>
        <p className="text-xs" style={{ color: '#848E9C' }}>
          {t('disabledHint', language)}
        </p>
      </div>

      {/* Max drawdown */}
      <div>
        <p className="text-xs font-medium mb-2" style={{ color: '#F0B90B' }}>
          {t('drawdown', language)}
        </p>
        <p className="text-xs mb-3" style={{ color: '#848E9C' }}>
          {t('drawdownDesc', language)}
        </p>
        <div className="grid grid-cols-2 gap-4">
          {numField('max_drawdown_pct', t('maxDrawdown', language), DEFAULTS.max_drawdown_pct, 1, 90, 1)}
          {numField('max_drawdown_lookback_h', t('lookback', language), DEFAULTS.max_drawdown_lookback_h, 1, 2160, 1)}
        </div>
        <div
          className="p-4 rounded-lg mt-4"
          style={{ background: '#0B0E11', border: '1px solid #2B3139' }}
        >
          <label className="block text-sm mb-2" style={{ color: '#EAECEF' }}>
            {t('basis', language)}
          </label>
          <select
            value={c.drawdown_basis ?? DEFAULTS.drawdown_basis}
            onChange={(e) => updateField('drawdown_basis', e.target.value as never)}
            disabled={disabled}
            className="w-full px-3 py-2 rounded"
            style={{
              background: '#1E2329',
              border: '1px solid #2B3139',
              color: '#EAECEF',
            }}
          >
            <option value="equity">{t('basisEquity', language)}</option>
            <option value="balance">{t('basisBalance', language)}</option>
          </select>
        </div>
      </div>

      {/* Losing streak */}
      <div>
        <p className="text-xs font-medium mb-2" style={{ color: '#F0B90B' }}>
          {t('losing', language)}
        </p>
        <p className="text-xs mb-3" style={{ color: '#848E9C' }}>
          {t('losingDesc', language)}
        </p>
        <div className="grid grid-cols-2 gap-4">
          {numField('losing_closes_guard_count', t('losingCount', language), DEFAULTS.losing_closes_guard_count, 1, 50, 1)}
          {numField('losing_closes_guard_lookback_h', t('lookback', language), DEFAULTS.losing_closes_guard_lookback_h, 1, 2160, 1)}
        </div>
      </div>

      {/* Cooldown */}
      <div>
        <p className="text-xs font-medium mb-2" style={{ color: '#F0B90B' }}>
          {t('cooldown', language)}
        </p>
        <p className="text-xs mb-3" style={{ color: '#848E9C' }}>
          {t('cooldownDesc', language)}
        </p>
        <div className="grid grid-cols-2 gap-4">
          {numField('cooldown_minutes', t('cooldownMin', language), DEFAULTS.cooldown_minutes, 0, 10080, 1)}
        </div>
      </div>

      {/* Low-profit pairs */}
      <div>
        <p className="text-xs font-medium mb-2" style={{ color: '#F0B90B' }}>
          {t('lowProfit', language)}
        </p>
        <p className="text-xs mb-3" style={{ color: '#848E9C' }}>
          {t('lowProfitDesc', language)}
        </p>
        <div className="grid grid-cols-2 gap-4">
          {numField('low_profit_min_trades', t('lowProfitMin', language), DEFAULTS.low_profit_min_trades, 1, 100, 1)}
          {numField('low_profit_lookback_h', t('lookback', language), DEFAULTS.low_profit_lookback_h, 1, 2160, 1)}
        </div>
      </div>
    </div>
  )
}
