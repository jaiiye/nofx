import { Activity, BarChart2, Clock, Info, Lock, SlidersHorizontal, TrendingUp } from 'lucide-react'
import type { IndicatorConfig } from '../../types'

interface IndicatorEditorProps {
  config: IndicatorConfig
  onChange: (config: IndicatorConfig) => void
  disabled?: boolean
  language: string
}

const t = (language: string, zh: string, en: string) => (language === 'zh' ? zh : en)

const timeframes = [
  { value: '1m', label: '1m', group: 'scalp' },
  { value: '3m', label: '3m', group: 'scalp' },
  { value: '5m', label: '5m', group: 'scalp' },
  { value: '15m', label: '15m', group: 'intraday' },
  { value: '30m', label: '30m', group: 'intraday' },
  { value: '1h', label: '1h', group: 'intraday' },
  { value: '2h', label: '2h', group: 'swing' },
  { value: '4h', label: '4h', group: 'swing' },
  { value: '1d', label: '1D', group: 'position' },
]

const groupLabels: Record<string, string> = {
  scalp: 'Scalp',
  intraday: 'Intraday',
  swing: 'Swing',
  position: 'Position',
}

// Period lists are edited as comma-separated text and committed on blur, so
// typing "50,200" is not re-parsed (and normalised) on every keystroke.
const listFields = [
  { key: 'ema_periods', label: 'EMA 周期', fallback: [20, 50] },
  { key: 'rsi_periods', label: 'RSI 周期', fallback: [7, 14] },
  { key: 'atr_periods', label: 'ATR 周期', fallback: [14] },
  { key: 'adx_periods', label: 'ADX 周期', fallback: [14] },
  { key: 'boll_periods', label: 'BOLL 周期', fallback: [20] },
] as const

const numberFields = [
  { key: 'macd_fast_period', label: 'MACD 快线', fallback: 12, step: 1 },
  { key: 'macd_slow_period', label: 'MACD 慢线', fallback: 26, step: 1 },
  { key: 'boll_multiplier', label: 'BOLL 倍数 (σ)', fallback: 2, step: 0.5 },
  { key: 'supertrend_period', label: 'Supertrend 周期', fallback: 10, step: 1 },
  { key: 'supertrend_multiplier', label: 'Supertrend 倍数', fallback: 3, step: 0.5 },
] as const

function parsePeriods(raw: string): number[] {
  return raw
    .split(',')
    .map((part) => Number(part.trim()))
    .filter((n) => Number.isFinite(n) && n > 0)
    .map((n) => Math.round(n))
}

const indicatorCards = [
  { key: 'enable_ema', label: 'EMA', hint: '20/50', color: '#F0B90B' },
  { key: 'enable_macd', label: 'MACD', hint: 'trend momentum', color: '#a855f7' },
  { key: 'enable_rsi', label: 'RSI', hint: 'overbought/oversold', color: '#F6465D' },
  { key: 'enable_atr', label: 'ATR', hint: 'volatility risk', color: '#60a5fa' },
  { key: 'enable_boll', label: 'BOLL', hint: 'range / breakout', color: '#ec4899' },
  { key: 'enable_adx', label: 'ADX', hint: 'trend strength (+DI/-DI)', color: '#22d3ee' },
  { key: 'enable_supertrend', label: 'Supertrend', hint: 'trailing trend direction', color: '#4ade80' },
] as const

const marketContextCards = [
  { key: 'enable_volume', label: 'Volume', hint: 'from Hyperliquid candle volume', color: '#c084fc' },
  { key: 'enable_oi', label: 'Open Interest', hint: 'native exchange context when available', color: '#34d399' },
  { key: 'enable_funding_rate', label: 'Funding', hint: 'perp funding context', color: '#fbbf24' },
  { key: 'enable_microstructure', label: 'Order Book', hint: 'spread / imbalance / depth / liquidity', color: '#38bdf8' },
] as const

export function IndicatorEditor({ config, onChange, disabled, language }: IndicatorEditorProps) {
  const selectedTimeframes = config.klines.selected_timeframes || [config.klines.primary_timeframe || '5m']

  const update = (patch: Partial<IndicatorConfig>) => {
    if (disabled) return
    onChange({
      ...config,
      // Ensure the simplified Hyperliquid strategy editor never enables NofxOSAI-only datasets.
      nofxos_api_key: '',
      enable_quant_data: false,
      enable_quant_oi: false,
      enable_quant_netflow: false,
      enable_oi_ranking: false,
      enable_netflow_ranking: false,
      enable_price_ranking: false,
      ...patch,
      enable_raw_klines: true,
    })
  }

  const toggleTimeframe = (tf: string) => {
    if (disabled) return
    const current = [...selectedTimeframes]
    const exists = current.includes(tf)
    if (exists && current.length === 1) return
    const next = exists ? current.filter((item) => item !== tf) : [...current, tf].slice(0, 4)
    const primary = next.includes(config.klines.primary_timeframe) ? config.klines.primary_timeframe : next[0]
    update({
      klines: {
        ...config.klines,
        selected_timeframes: next,
        primary_timeframe: primary,
        enable_multi_timeframe: next.length > 1,
      },
    })
  }

  const setPrimary = (tf: string) => {
    if (disabled || !selectedTimeframes.includes(tf)) return
    update({ klines: { ...config.klines, primary_timeframe: tf } })
  }

  const toggleBool = (key: keyof IndicatorConfig) => {
    update({ [key]: !config[key] } as Partial<IndicatorConfig>)
  }

  return (
    <div className="space-y-5">
      <div className="rounded-2xl border border-sky-400/20 bg-sky-500/5 p-4">
        <div className="flex items-center gap-2 text-nofx-text">
          <BarChart2 className="h-5 w-5 text-sky-300" />
          <h3 className="font-semibold">{t(language, '真实行情输入', 'Real market inputs')}</h3>
        </div>
        <p className="mt-1 text-xs leading-5 text-nofx-text-muted">
          {t(
            language,
            'AI 只喂 Hyperliquid 原生 K 线、成交量、资金费率/持仓等交易所可用数据；不再混入外部聚合数据。',
            'AI uses native Hyperliquid candles, volume, funding/OI when available. External aggregate datasets are not mixed in.'
          )}
        </p>
      </div>

      <div className="rounded-2xl border border-white/10 bg-nofx-bg-lighter p-4">
        <div className="mb-4 flex items-start justify-between gap-3">
          <div>
            <div className="flex items-center gap-2 text-sm font-semibold text-nofx-text">
              <TrendingUp className="h-4 w-4 text-nofx-gold" />
              {t(language, 'K 线数据', 'Candles')}
              <span className="inline-flex items-center gap-1 rounded-full bg-nofx-gold/15 px-2 py-0.5 text-[10px] text-nofx-gold">
                <Lock className="h-3 w-3" />
                {t(language, '必需', 'Required')}
              </span>
            </div>
            <p className="mt-1 text-xs text-nofx-text-muted">
              {t(language, '来自 Hyperliquid candleSnapshot。最多选择 4 个时间周期。', 'From Hyperliquid candleSnapshot. Select up to 4 timeframes.')}
            </p>
          </div>
          <div className="flex items-center gap-2 text-xs text-nofx-text-muted">
            {t(language, '根数', 'Bars')}
            <input
              type="number"
              min={10}
              max={200}
              value={config.klines.primary_count || 20}
              disabled={disabled}
              onChange={(e) => update({ klines: { ...config.klines, primary_count: Number(e.target.value) || 20 } })}
              className="w-16 rounded-lg border border-white/10 bg-nofx-bg px-2 py-1 text-center text-nofx-text"
            />
          </div>
        </div>

        <div className="space-y-3">
          {['scalp', 'intraday', 'swing', 'position'].map((group) => (
            <div key={group} className="flex items-center gap-3">
              <span className="w-16 text-[11px] text-nofx-text-muted">{groupLabels[group]}</span>
              <div className="flex flex-wrap gap-2">
                {timeframes.filter((tf) => tf.group === group).map((tf) => {
                  const selected = selectedTimeframes.includes(tf.value)
                  const primary = config.klines.primary_timeframe === tf.value
                  return (
                    <button
                      key={tf.value}
                      type="button"
                      disabled={disabled}
                      onClick={() => toggleTimeframe(tf.value)}
                      onDoubleClick={() => setPrimary(tf.value)}
                      className={`rounded-lg border px-3 py-1.5 text-xs transition-all ${
                        selected
                          ? 'border-nofx-gold bg-nofx-gold/10 text-nofx-gold'
                          : 'border-white/10 bg-white/[0.02] text-nofx-text-muted hover:text-white'
                      }`}
                      title={primary ? 'Primary timeframe' : 'Double click selected item to make primary'}
                    >
                      {tf.label}{primary && ' ★'}
                    </button>
                  )
                })}
              </div>
            </div>
          ))}
        </div>
      </div>

      <div className="rounded-2xl border border-white/10 bg-nofx-bg-lighter p-4">
        <div className="mb-3 flex items-center gap-2 text-sm font-semibold text-nofx-text">
          <Activity className="h-4 w-4 text-nofx-success" />
          {t(language, '可选技术指标', 'Optional technical indicators')}
        </div>
        <div className="mb-3 flex items-start gap-2 rounded-xl bg-nofx-success/5 p-3 text-xs text-nofx-text-muted">
          <Info className="mt-0.5 h-3.5 w-3.5 flex-shrink-0 text-nofx-success" />
          {t(language, '默认只给原始 K 线，AI 可以自己计算。需要固定指标时再开启。', 'Raw candles are enough by default; enable fixed indicators only when needed.')}
        </div>
        <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
          {indicatorCards.map(({ key, label, hint, color }) => {
            const enabled = Boolean(config[key])
            return (
              <button
                key={key}
                type="button"
                disabled={disabled}
                onClick={() => toggleBool(key)}
                className={`rounded-xl border p-3 text-left transition-all ${enabled ? 'bg-white/[0.04]' : 'bg-transparent hover:bg-white/[0.03]'}`}
                style={{ borderColor: enabled ? `${color}66` : 'rgba(255,255,255,0.1)' }}
              >
                <div className="flex items-center justify-between text-sm font-medium text-nofx-text">
                  <span>{label}</span>
                  <span className="h-2 w-2 rounded-full" style={{ background: enabled ? color : '#5E6673' }} />
                </div>
                <div className="mt-1 text-[11px] text-nofx-text-muted">{hint}</div>
              </button>
            )
          })}
        </div>
      </div>

      {/* Indicator parameters: without these the periods stay at their defaults
          (config plumbing exists server-side, but there was no way to edit it). */}
      <div className="rounded-2xl border border-white/10 bg-nofx-bg-lighter p-4">
        <div className="mb-1 flex items-center gap-2 text-sm font-semibold text-nofx-text">
          <SlidersHorizontal className="h-4 w-4 text-sky-300" />
          {t(language, '指标参数', 'Indicator parameters')}
        </div>
        <p className="mb-3 text-xs leading-5 text-nofx-text-muted">
          {t(
            language,
            '多项周期用逗号分隔（如 50,200），留空表示使用默认值。周期基于主时间帧计算；1h 主帧要用 EMA200 需把根数设为 200。',
            'Separate multiple periods with commas (e.g. 50,200); leave blank for defaults. Periods are computed on the primary timeframe; EMA200 on a 1h primary needs 200 bars.'
          )}
        </p>
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {listFields.map(({ key, label, fallback }) => (
            <label key={key} className="text-xs text-nofx-text-muted">
              <span className="mb-1 block">{label}</span>
              <input
                type="text"
                key={`${key}-${(config[key] ?? fallback).join(',')}`}
                defaultValue={(config[key] ?? fallback).join(',')}
                disabled={disabled}
                onBlur={(e) => {
                  const parsed = parsePeriods(e.target.value)
                  update({ [key]: parsed.length > 0 ? parsed : fallback } as Partial<IndicatorConfig>)
                }}
                className="w-full rounded-lg px-3 py-2 font-mono"
                style={{ background: '#1E2329', border: '1px solid #2B3139', color: '#EAEFEF' }}
              />
            </label>
          ))}
          {numberFields.map(({ key, label, fallback, step }) => (
            <label key={key} className="text-xs text-nofx-text-muted">
              <span className="mb-1 block">{label}</span>
              <input
                type="number"
                key={`${key}-${config[key] ?? fallback}`}
                defaultValue={config[key] ?? fallback}
                disabled={disabled}
                step={step ?? 1}
                min={0}
                onBlur={(e) => {
                  const parsed = Number(e.target.value)
                  update({ [key]: Number.isFinite(parsed) && parsed > 0 ? parsed : fallback } as Partial<IndicatorConfig>)
                }}
                className="w-full rounded-lg px-3 py-2 font-mono"
                style={{ background: '#1E2329', border: '1px solid #2B3139', color: '#EAEFEF' }}
              />
            </label>
          ))}
          <label className="text-xs text-nofx-text-muted">
            <span className="mb-1 block">
              {t(language, '展示上限（每周期打印的根数/值数）', 'Display cap (bars/values printed per timeframe)')}
            </span>
            <input
              type="number"
              key={`display-${config.klines.prompt_display_bars ?? 40}`}
              defaultValue={config.klines.prompt_display_bars ?? 40}
              disabled={disabled}
              min={5}
              max={200}
              step={5}
              onBlur={(e) => {
                const parsed = Number(e.target.value)
                update({
                  klines: {
                    ...config.klines,
                    prompt_display_bars: Number.isFinite(parsed) && parsed >= 5 ? parsed : 40,
                  },
                })
              }}
              className="w-full rounded-lg px-3 py-2 font-mono"
              style={{ background: '#1E2329', border: '1px solid #2B3139', color: '#EAEFEF' }}
            />
          </label>
        </div>
      </div>

      <div className="rounded-2xl border border-white/10 bg-nofx-bg-lighter p-4">
        <div className="mb-3 flex items-center gap-2 text-sm font-semibold text-nofx-text">
          <Clock className="h-4 w-4 text-amber-300" />
          {t(language, '交易所上下文', 'Exchange context')}
        </div>
        <div className="grid gap-2 sm:grid-cols-3">
          {marketContextCards.map(({ key, label, hint, color }) => {
            // Microstructure defaults to enabled when the key is absent.
            const enabled = key === 'enable_microstructure'
              ? config.enable_microstructure !== false
              : Boolean(config[key])
            return (
              <button
                key={key}
                type="button"
                disabled={disabled}
                onClick={() => toggleBool(key)}
                className={`rounded-xl border p-3 text-left transition-all ${enabled ? 'bg-white/[0.04]' : 'bg-transparent hover:bg-white/[0.03]'}`}
                style={{ borderColor: enabled ? `${color}66` : 'rgba(255,255,255,0.1)' }}
              >
                <div className="text-sm font-medium text-nofx-text">{label}</div>
                <div className="mt-1 text-[11px] text-nofx-text-muted">{hint}</div>
              </button>
            )
          })}
        </div>
      </div>
    </div>
  )
}
