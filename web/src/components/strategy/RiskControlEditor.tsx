import { Shield, AlertTriangle } from 'lucide-react'
import type { RiskControlConfig } from '../../types'
import { riskControl, ts } from '../../i18n/strategy-translations'

interface RiskControlEditorProps {
  config: RiskControlConfig
  onChange: (config: RiskControlConfig) => void
  disabled?: boolean
  language: string
}

export function RiskControlEditor({
  config,
  onChange,
  disabled,
  language,
}: RiskControlEditorProps) {
  const updateField = <K extends keyof RiskControlConfig>(
    key: K,
    value: RiskControlConfig[K]
  ) => {
    if (!disabled) {
      onChange({ ...config, [key]: value })
    }
  }

  return (
    <div className="space-y-6">
      {/* Position Limits */}
      <div>
        <div className="flex items-center gap-2 mb-4">
          <Shield className="w-5 h-5" style={{ color: '#F0B90B' }} />
          <h3 className="font-medium" style={{ color: '#EAECEF' }}>
            {ts(riskControl.positionLimits, language)}
          </h3>
        </div>

        <div className="grid grid-cols-1 gap-4 mb-4">
          <div
            className="p-4 rounded-lg"
            style={{ background: '#0B0E11', border: '1px solid #0ECB81' }}
          >
            <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>
              {ts(riskControl.maxPositions, language)}
            </label>
            <p className="text-xs mb-2" style={{ color: '#848E9C' }}>
              {ts(riskControl.maxPositionsDesc, language)}
            </p>
            <div className="flex items-center gap-3">
              <span className="font-mono text-lg" style={{ color: '#0ECB81' }}>
                {config.max_positions ?? 3}
              </span>
              <span className="text-xs" style={{ color: '#848E9C' }}>
                System enforced
              </span>
            </div>
          </div>
        </div>

        {/* Trading Leverage (Exchange) */}
        <div className="mb-2">
          <p className="text-xs font-medium mb-2" style={{ color: '#F0B90B' }}>
            {ts(riskControl.tradingLeverage, language)}
          </p>
        </div>
        <div className="grid grid-cols-2 gap-4 mb-4">
          <div
            className="p-4 rounded-lg"
            style={{ background: '#0B0E11', border: '1px solid #2B3139' }}
          >
            <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>
              {ts(riskControl.btcEthLeverage, language)}
            </label>
            <p className="text-xs mb-2" style={{ color: '#848E9C' }}>
              {ts(riskControl.btcEthLeverageDesc, language)}
            </p>
            <div className="flex items-center gap-2">
              <input
                type="range"
                value={config.btc_eth_max_leverage ?? 5}
                onChange={(e) =>
                  updateField('btc_eth_max_leverage', parseInt(e.target.value))
                }
                disabled={disabled}
                min={1}
                max={20}
                className="flex-1 accent-yellow-500"
              />
              <span
                className="w-12 text-center font-mono"
                style={{ color: '#F0B90B' }}
              >
                {config.btc_eth_max_leverage ?? 5}x
              </span>
            </div>
          </div>

          <div
            className="p-4 rounded-lg"
            style={{ background: '#0B0E11', border: '1px solid #2B3139' }}
          >
            <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>
              {ts(riskControl.altcoinLeverage, language)}
            </label>
            <p className="text-xs mb-2" style={{ color: '#848E9C' }}>
              {ts(riskControl.altcoinLeverageDesc, language)}
            </p>
            <div className="flex items-center gap-2">
              <input
                type="range"
                value={config.altcoin_max_leverage ?? 5}
                onChange={(e) =>
                  updateField('altcoin_max_leverage', parseInt(e.target.value))
                }
                disabled={disabled}
                min={1}
                max={20}
                className="flex-1 accent-yellow-500"
              />
              <span
                className="w-12 text-center font-mono"
                style={{ color: '#F0B90B' }}
              >
                {config.altcoin_max_leverage ?? 5}x
              </span>
            </div>
          </div>
        </div>

        {/* Position Value Ratio (Risk Control - CODE ENFORCED) */}
        <div className="mb-2">
          <p className="text-xs font-medium" style={{ color: '#0ECB81' }}>
            {ts(riskControl.positionValueRatio, language)}
          </p>
          <p className="text-xs mt-1" style={{ color: '#848E9C' }}>
            {ts(riskControl.positionValueRatioDesc, language)}
          </p>
        </div>
        <div className="grid grid-cols-2 gap-4">
          <div
            className="p-4 rounded-lg"
            style={{ background: '#0B0E11', border: '1px solid #0ECB81' }}
          >
            <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>
              {ts(riskControl.btcEthPositionValueRatio, language)}
            </label>
            <p className="text-xs mb-2" style={{ color: '#848E9C' }}>
              {ts(riskControl.btcEthPositionValueRatioDesc, language)}
            </p>
            <div className="flex items-center gap-2">
              <span
                className="w-12 text-center font-mono"
                style={{ color: '#0ECB81' }}
              >
                {config.btc_eth_max_position_value_ratio ?? 5}x
              </span>
              <span className="text-xs" style={{ color: '#848E9C' }}>
                System enforced
              </span>
            </div>
          </div>

          <div
            className="p-4 rounded-lg"
            style={{ background: '#0B0E11', border: '1px solid #0ECB81' }}
          >
            <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>
              {ts(riskControl.altcoinPositionValueRatio, language)}
            </label>
            <p className="text-xs mb-2" style={{ color: '#848E9C' }}>
              {ts(riskControl.altcoinPositionValueRatioDesc, language)}
            </p>
            <div className="flex items-center gap-2">
              <span
                className="w-12 text-center font-mono"
                style={{ color: '#0ECB81' }}
              >
                {config.altcoin_max_position_value_ratio ?? 1}x
              </span>
              <span className="text-xs" style={{ color: '#848E9C' }}>
                System enforced
              </span>
            </div>
          </div>
        </div>
      </div>

      {/* Risk Parameters */}
      <div>
        <div className="flex items-center gap-2 mb-4">
          <AlertTriangle className="w-5 h-5" style={{ color: '#F6465D' }} />
          <h3 className="font-medium" style={{ color: '#EAECEF' }}>
            {ts(riskControl.riskParameters, language)}
          </h3>
        </div>

        <div className="grid grid-cols-2 gap-4">
          <div
            className="p-4 rounded-lg"
            style={{ background: '#0B0E11', border: '1px solid #2B3139' }}
          >
            <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>
              {ts(riskControl.minRiskReward, language)}
            </label>
            <p className="text-xs mb-2" style={{ color: '#848E9C' }}>
              {ts(riskControl.minRiskRewardDesc, language)}
            </p>
            <div className="flex items-center">
              <span style={{ color: '#848E9C' }}>1:</span>
              <input
                type="number"
                value={config.min_risk_reward_ratio ?? 3}
                onChange={(e) =>
                  updateField('min_risk_reward_ratio', parseFloat(e.target.value) || 3)
                }
                disabled={disabled}
                min={1}
                max={10}
                step={0.5}
                className="w-20 px-3 py-2 rounded ml-2"
                style={{
                  background: '#1E2329',
                  border: '1px solid #2B3139',
                  color: '#EAECEF',
                }}
              />
            </div>
          </div>

          <div
            className="p-4 rounded-lg"
            style={{ background: '#0B0E11', border: '1px solid #0ECB81' }}
          >
            <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>
              {ts(riskControl.maxMarginUsage, language)}
            </label>
            <p className="text-xs mb-2" style={{ color: '#848E9C' }}>
              {ts(riskControl.maxMarginUsageDesc, language)}
            </p>
            <div className="flex items-center gap-2">
              <input
                type="number"
                value={Math.round((config.max_margin_usage ?? 0.9) * 100)}
                onChange={(e) => {
                  const percent = Number(e.target.value)
                  updateField('max_margin_usage', Number.isFinite(percent) ? percent / 100 : 0.9)
                }}
                disabled={disabled}
                min={10}
                max={100}
                step={5}
                className="w-20 px-3 py-2 rounded"
                style={{
                  background: '#1E2329',
                  border: '1px solid #2B3139',
                  color: '#EAECEF',
                }}
              />
              <span className="font-mono" style={{ color: '#0ECB81' }}>
                %
              </span>
              <span className="text-xs" style={{ color: '#848E9C' }}>
                System enforced
              </span>
            </div>
          </div>
        </div>
      </div>

      {/* Entry Requirements */}
      <div>
        <div className="flex items-center gap-2 mb-4">
          <Shield className="w-5 h-5" style={{ color: '#0ECB81' }} />
          <h3 className="font-medium" style={{ color: '#EAECEF' }}>
            {ts(riskControl.entryRequirements, language)}
          </h3>
        </div>

        <div className="grid grid-cols-2 gap-4">
          <div
            className="p-4 rounded-lg"
            style={{ background: '#0B0E11', border: '1px solid #0ECB81' }}
          >
            <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>
              {ts(riskControl.minPositionSize, language)}
            </label>
            <p className="text-xs mb-2" style={{ color: '#848E9C' }}>
              {ts(riskControl.minPositionSizeDesc, language)}
            </p>
            <div className="flex items-center gap-2">
              <span className="font-mono text-lg" style={{ color: '#0ECB81' }}>
                {config.min_position_size ?? 12}
              </span>
              <span className="ml-2" style={{ color: '#848E9C' }}>
                USDT
              </span>
              <span className="text-xs" style={{ color: '#848E9C' }}>
                System enforced
              </span>
            </div>
          </div>

          <div
            className="p-4 rounded-lg"
            style={{ background: '#0B0E11', border: '1px solid #2B3139' }}
          >
            <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>
              {ts(riskControl.minConfidence, language)}
            </label>
            <p className="text-xs mb-2" style={{ color: '#848E9C' }}>
              {ts(riskControl.minConfidenceDesc, language)}
            </p>
            <div className="flex items-center gap-2">
              <input
                type="range"
                value={config.min_confidence ?? 75}
                onChange={(e) =>
                  updateField('min_confidence', parseInt(e.target.value))
                }
                disabled={disabled}
                min={50}
                max={100}
                className="flex-1 accent-green-500"
              />
              <span className="w-12 text-center font-mono" style={{ color: '#0ECB81' }}>
                {config.min_confidence ?? 75}
              </span>
            </div>
          </div>

          {/* Entry order mode. Backend-enforced: an unset or unrecognized value
              means market, and the wait is clamped to 5-60s. */}
          <div className="p-3 rounded-lg" style={{ backgroundColor: 'rgba(255,255,255,0.02)' }}>
            <div className="flex items-center justify-between mb-2">
              <span className="text-sm font-medium" style={{ color: '#EAEFEF' }}>
                {language === 'zh' ? '入场方式' : 'Entry order mode'}
              </span>
              <select
                value={config.entry_order_mode ?? 'market'}
                onChange={(e) => updateField('entry_order_mode', e.target.value as 'market' | 'maker')}
                disabled={disabled}
                className="rounded-md px-2 py-1 text-xs outline-none disabled:opacity-50"
                style={{
                  background: 'rgba(240,185,11,0.08)',
                  border: '1px solid rgba(240,185,11,0.25)',
                  color: '#F0B90B',
                }}
              >
                <option value="market">{language === 'zh' ? '市价（IOC）' : 'Market (IOC)'}</option>
                <option value="maker">{language === 'zh' ? '挂单（post-only）' : 'Post-only (maker)'}</option>
              </select>
            </div>
            <p className="text-xs mb-2" style={{ color: '#848E9C' }}>
              {language === 'zh'
                ? '挂单会先在盘口被动侧挂 ALO 限价单以支付 maker 费率；超时未成交则撤单并回退市价。仅作用于开仓，平仓始终为市价；部分成交按已成交量建仓。'
                : 'Post-only first rests an ALO limit on the passive side of the book to pay the maker fee; on timeout it cancels and falls back to the market order. Opening orders only — closes stay aggressive, and a partial fill is kept as-is.'}
            </p>
            <div className="flex items-center gap-2">
              <span className="text-xs" style={{ color: '#848E9C' }}>
                {language === 'zh' ? '等待秒数' : 'Wait (s)'}
              </span>
              <input
                type="number"
                min={5}
                max={60}
                value={config.maker_entry_timeout_sec ?? 20}
                disabled={disabled || (config.entry_order_mode ?? 'market') !== 'maker'}
                onChange={(e) =>
                  updateField(
                    'maker_entry_timeout_sec',
                    Math.min(60, Math.max(5, Number(e.target.value) || 20))
                  )
                }
                className="w-20 rounded-md px-2 py-1 text-xs outline-none disabled:opacity-50"
                style={{ background: '#1E2329', border: '1px solid #2B3139', color: '#EAEFEF' }}
              />
            </div>
          </div>

          {/* Turnover is the cost multiplier that survives every signal test:
              the short-hold bucket is the worst one in live data. Discretionary
              exits before the minimum are ignored; protective exits are not. */}
          <div className="p-3 rounded-lg" style={{ backgroundColor: 'rgba(255,255,255,0.02)' }}>
            <div className="flex items-center justify-between mb-2">
              <span className="text-sm font-medium" style={{ color: '#EAEFEF' }}>
                {language === 'zh' ? '最短持仓' : 'Minimum hold'}
              </span>
              <div className="flex items-center gap-2">
                <input
                  type="number"
                  min={0}
                  max={1440}
                  value={config.min_hold_minutes ?? 0}
                  disabled={disabled}
                  onChange={(e) =>
                    updateField('min_hold_minutes', Math.min(1440, Math.max(0, Number(e.target.value) || 0)))
                  }
                  className="w-20 rounded-md px-2 py-1 text-xs outline-none disabled:opacity-50"
                  style={{ background: '#1E2329', border: '1px solid #2B3139', color: '#EAEFEF' }}
                />
                <span className="text-xs" style={{ color: '#848E9C' }}>{language === 'zh' ? '分钟' : 'min'}</span>
              </div>
            </div>
            <p className="text-xs" style={{ color: '#848E9C' }}>
              {language === 'zh'
                ? '填 0 表示关闭（默认）；填正数后，不足该时长的「主动平仓」会被忽略。止损/止盈与回撤监控不受影响——保护性出场永远立即执行。'
                : '0 = off (default); a positive value ignores discretionary AI closes taken before it. Stop-loss, take-profit and the drawdown monitor are unaffected — protective exits always execute immediately.'}
            </p>
          </div>

          {/* Stock session gate: a stock perp quotes around the clock while its
              underlying does not, so an entry into a closed market faces an
              overnight gap that a stop cannot protect. */}
          <div className="p-3 rounded-lg" style={{ backgroundColor: 'rgba(255,255,255,0.02)' }}>
            <div className="flex items-center justify-between mb-2">
              <span className="text-sm font-medium" style={{ color: '#EAEFEF' }}>
                {language === 'zh' ? '闭市保护（股票）' : 'Stock session gate'}
              </span>
              <button
                type="button"
                onClick={() =>
                  updateField('block_cold_stock_entries', config.block_cold_stock_entries === false)
                }
                disabled={disabled}
                className={`rounded-md px-2 py-1 text-xs ${config.block_cold_stock_entries === false ? '' : 'font-medium'}`}
                style={{
                  background:
                    config.block_cold_stock_entries === false
                      ? 'rgba(255,255,255,0.04)'
                      : 'rgba(14,203,129,0.12)',
                  border: '1px solid rgba(255,255,255,0.12)',
                  color: config.block_cold_stock_entries === false ? '#848E9C' : '#0ECB81',
                }}
              >
                {config.block_cold_stock_entries === false
                  ? language === 'zh'
                    ? '已关闭'
                    : 'Off'
                  : language === 'zh'
                    ? '已开启'
                    : 'On'}
              </button>
            </div>
            <p className="text-xs mb-2" style={{ color: '#848E9C' }}>
              {language === 'zh'
                ? '股票永续全天报价，但其现货市场会休市：闭市期间开仓会遇到跳空，止损无法保护。开启后，当交易所长时间无成交时不再开新仓（平仓不受影响）。'
                : 'A stock perp quotes around the clock while its underlying does not: entering while the cash market is closed faces a gap that a stop cannot protect. When on, new entries are skipped if the venue shows no trades; closes are never affected.'}
            </p>
            <div className="flex items-center gap-2">
              <span className="text-xs" style={{ color: '#848E9C' }}>
                {language === 'zh' ? '静默分钟' : 'Quiet (min)'}
              </span>
              <input
                type="number"
                min={5}
                max={120}
                value={config.stock_quiet_minutes ?? 30}
                disabled={disabled || config.block_cold_stock_entries === false}
                onChange={(e) =>
                  updateField(
                    'stock_quiet_minutes',
                    Math.min(120, Math.max(5, Number(e.target.value) || 30))
                  )
                }
                className="w-20 rounded-md px-2 py-1 text-xs outline-none disabled:opacity-50"
                style={{ background: '#1E2329', border: '1px solid #2B3139', color: '#EAEFEF' }}
              />
            </div>
          </div>

          {/* Three correlated names are one bet with three sets of fees. The cap
              counts a same-side cluster as one bet and refuses the entry that
              would exceed it. */}
          <div className="p-3 rounded-lg" style={{ backgroundColor: 'rgba(255,255,255,0.02)' }}>
            <div className="flex items-center justify-between mb-2">
              <span className="text-sm font-medium" style={{ color: '#EAEFEF' }}>
                {language === 'zh' ? '相关性集中度' : 'Correlated exposure'}
              </span>
              <span className="text-[11px]" style={{ color: '#848E9C' }}>
                {language === 'zh' ? '同向 + 高相关 = 一注' : 'same side + high ρ = one bet'}
              </span>
            </div>
            <div className="flex items-center gap-3">
              <span className="text-xs" style={{ color: '#848E9C' }}>
                {language === 'zh' ? '每簇上限' : 'Per-cluster cap'}
              </span>
              <input
                type="number"
                min={-1}
                max={3}
                value={config.max_correlated_positions ?? 2}
                disabled={disabled}
                onChange={(e) => updateField('max_correlated_positions', Math.min(3, Math.max(-1, Number(e.target.value) || 0)))}
                className="w-16 rounded-md px-2 py-1 text-xs outline-none disabled:opacity-50"
                style={{ background: '#1E2329', border: '1px solid #2B3139', color: '#EAEFEF' }}
              />
              <span className="text-xs" style={{ color: '#848E9C' }}>ρ ≥</span>
              <input
                type="number"
                step={0.05}
                min={0.5}
                max={0.99}
                value={config.correlation_threshold ?? 0.8}
                disabled={disabled}
                onChange={(e) =>
                  updateField('correlation_threshold', Math.min(0.99, Math.max(0.5, Number(e.target.value) || 0.8)))
                }
                className="w-20 rounded-md px-2 py-1 text-xs outline-none disabled:opacity-50"
                style={{ background: '#1E2329', border: '1px solid #2B3139', color: '#EAEFEF' }}
              />
            </div>
            <p className="text-xs mt-1" style={{ color: '#848E9C' }}>
              {language === 'zh'
                ? '在决策上下文的收盘 K 线上计算（无额外请求）；数据不足时放行。上限填 -1 可关闭该检查。'
                : 'Measured on the closed bars already in the decision context (no extra request); fails open without enough history. Set the cap to -1 to disable.'}
            </p>
          </div>
        </div>
      </div>
    </div>
  )
}
