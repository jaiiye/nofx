import React, { useState, useEffect } from 'react'
import type { Exchange } from '../../types'
import { t, type Language } from '../../i18n/translations'
import { useAuth } from '../../contexts/AuthContext'
import { HyperliquidWalletConnect } from '../common/HyperliquidWalletConnect'
import { getExchangeIcon } from '../common/ExchangeIcons'
import {
  WebCryptoEnvironmentCheck,
  type WebCryptoCheckStatus,
} from '../common/WebCryptoEnvironmentCheck'
import { Trash2, Key, Shield, ChevronLeft, Check } from 'lucide-react'
import { getShortName } from './utils'

// Supported exchange templates (this build only ships Hyperliquid)
const SUPPORTED_EXCHANGE_TEMPLATES = [
  { exchange_type: 'hyperliquid', name: 'Hyperliquid', type: 'dex' as const },
]

interface ExchangeConfigModalProps {
  allExchanges: Exchange[]
  editingExchangeId: string | null
  onSave: (
    exchangeId: string | null,
    exchangeType: string,
    accountName: string,
    apiKey: string,
    secretKey?: string,
    passphrase?: string,
    testnet?: boolean,
    hyperliquidWalletAddr?: string,
    hyperliquidUnifiedAccount?: boolean
  ) => Promise<void>
  onDelete: (exchangeId: string) => void
  onClose: () => void
  language: Language
}

// Step indicator component
function StepIndicator({ currentStep, labels }: { currentStep: number; labels: string[] }) {
  return (
    <div className="flex items-center justify-center gap-2 mb-6">
      {labels.map((label, index) => (
        <React.Fragment key={index}>
          <div className="flex items-center gap-2">
            <div
              className="w-8 h-8 rounded-full flex items-center justify-center text-sm font-bold transition-all"
              style={{
                background: index < currentStep ? '#0ECB81' : index === currentStep ? '#F0B90B' : '#2B3139',
                color: index <= currentStep ? '#000' : '#848E9C',
              }}
            >
              {index < currentStep ? <Check className="w-4 h-4" /> : index + 1}
            </div>
            <span
              className="text-xs font-medium hidden sm:block"
              style={{ color: index === currentStep ? '#EAECEF' : '#848E9C' }}
            >
              {label}
            </span>
          </div>
          {index < labels.length - 1 && (
            <div
              className="w-8 h-0.5 mx-1"
              style={{ background: index < currentStep ? '#0ECB81' : '#2B3139' }}
            />
          )}
        </React.Fragment>
      ))}
    </div>
  )
}

// Exchange card component
function ExchangeCard({
  template,
  selected,
  onClick,
  disabled,
}: {
  template: typeof SUPPORTED_EXCHANGE_TEMPLATES[0]
  selected: boolean
  onClick: () => void
  disabled?: boolean
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className="flex flex-col items-center gap-2 p-4 rounded-xl transition-all hover:scale-105 disabled:opacity-50 disabled:cursor-not-allowed disabled:hover:scale-100"
      style={{
        background: selected ? 'rgba(240, 185, 11, 0.15)' : '#0B0E11',
        border: selected ? '2px solid #F0B90B' : '2px solid #2B3139',
      }}
    >
      <div className="relative">
        {getExchangeIcon(template.exchange_type, { width: 48, height: 48 })}
        {selected && (
          <div
            className="absolute -top-1 -right-1 w-5 h-5 rounded-full flex items-center justify-center"
            style={{ background: '#0ECB81' }}
          >
            <Check className="w-3 h-3 text-black" />
          </div>
        )}
      </div>
      <span className="text-sm font-semibold" style={{ color: '#EAECEF' }}>
        {getShortName(template.name)}
      </span>
      <span
        className="text-xs px-2 py-0.5 rounded-full"
        style={{
          background: 'rgba(139, 92, 246, 0.2)',
          color: '#A78BFA',
        }}
      >
        {template.type.toUpperCase()}
      </span>
    </button>
  )
}

export function ExchangeConfigModal({
  allExchanges,
  editingExchangeId,
  onDelete,
  onClose,
  language,
}: ExchangeConfigModalProps) {
  const { user } = useAuth()
  // Step: 0 = select exchange, 1 = configure
  const [currentStep, setCurrentStep] = useState(editingExchangeId ? 1 : 0)
  const [selectedExchangeType, setSelectedExchangeType] = useState('')
  const [accountName, setAccountName] = useState('')
  const [accountNameInitialized, setAccountNameInitialized] = useState(false)
  const [webCryptoStatus, setWebCryptoStatus] = useState<WebCryptoCheckStatus>('idle')

  const selectedExchange = editingExchangeId
    ? allExchanges?.find((e) => e.id === editingExchangeId)
    : null

  const selectedTemplate = editingExchangeId
    ? SUPPORTED_EXCHANGE_TEMPLATES.find((tpl) => tpl.exchange_type === selectedExchange?.exchange_type)
    : SUPPORTED_EXCHANGE_TEMPLATES.find((tpl) => tpl.exchange_type === selectedExchangeType)

  const currentExchangeType = editingExchangeId
    ? selectedExchange?.exchange_type
    : selectedExchangeType

  // Initialize form when editing
  useEffect(() => {
    if (editingExchangeId && selectedExchange && !accountNameInitialized) {
      setAccountName(selectedExchange.account_name || '')
      setAccountNameInitialized(true)
    }
  }, [editingExchangeId, selectedExchange, accountNameInitialized])

  const handleSelectExchange = (exchangeType: string) => {
    setSelectedExchangeType(exchangeType)
    setCurrentStep(1)
  }

  const handleBack = () => {
    if (editingExchangeId) {
      onClose()
    } else {
      setCurrentStep(0)
      setSelectedExchangeType('')
    }
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    // This build only supports Hyperliquid, which is connected through the
    // wallet authorization flow rather than a manual credential form.
    return
  }

  const stepLabels = [t('exchangeConfig.selectExchange', language), t('exchangeConfig.configure', language)]

  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-50 p-4 overflow-y-auto backdrop-blur-sm">
      <div
        className="rounded-2xl w-full max-w-2xl relative my-8 shadow-2xl"
        style={{ background: 'linear-gradient(180deg, #1E2329 0%, #181A20 100%)', maxHeight: 'calc(100vh - 4rem)' }}
      >
        {/* Header */}
        <div className="flex items-center justify-between p-6 pb-2">
          <div className="flex items-center gap-3">
            {currentStep > 0 && !editingExchangeId && (
              <button type="button" onClick={handleBack} className="p-2 rounded-lg hover:bg-white/10 transition-colors">
                <ChevronLeft className="w-5 h-5" style={{ color: '#848E9C' }} />
              </button>
            )}
            <h3 className="text-xl font-bold" style={{ color: '#EAECEF' }}>
              {editingExchangeId ? t('editExchange', language) : t('addExchange', language)}
            </h3>
          </div>
          <div className="flex items-center gap-2">
            {editingExchangeId && (
              <button
                type="button"
                onClick={() => onDelete(editingExchangeId)}
                className="p-2 rounded-lg hover:bg-red-500/20 transition-colors"
                style={{ color: '#F6465D' }}
              >
                <Trash2 className="w-4 h-4" />
              </button>
            )}
            <button type="button" onClick={onClose} className="p-2 rounded-lg hover:bg-white/10 transition-colors" style={{ color: '#848E9C' }}>
              ✕
            </button>
          </div>
        </div>

        {/* Step Indicator */}
        {!editingExchangeId && (
          <div className="px-6">
            <StepIndicator currentStep={currentStep} labels={stepLabels} />
          </div>
        )}

        {/* Content */}
        <div className="px-6 pb-6 overflow-y-auto" style={{ maxHeight: 'calc(100vh - 16rem)' }}>
          {/* Step 0: Select Exchange */}
          {currentStep === 0 && !editingExchangeId && (
            <div className="space-y-6">
              {/* WebCrypto Check */}
              <div className="space-y-2">
                <div className="flex items-center gap-2 text-xs font-semibold uppercase tracking-wide" style={{ color: '#848E9C' }}>
                  <Shield className="w-4 h-4" />
                  {t('environmentSteps.checkTitle', language)}
                </div>
                <WebCryptoEnvironmentCheck language={language} variant="card" onStatusChange={setWebCryptoStatus} />
              </div>

              {/* Exchange Grid */}
              <div className="space-y-4">
                <div className="text-sm font-semibold" style={{ color: '#EAECEF' }}>
                  {t('exchangeConfig.chooseExchange', language)}
                </div>

                <div className="space-y-3">
                  <div className="text-xs font-medium uppercase tracking-wide" style={{ color: '#A78BFA' }}>
                    {t('exchangeConfig.decentralizedExchanges', language)}
                  </div>
                  <div className="grid grid-cols-3 sm:grid-cols-5 gap-3">
                    {SUPPORTED_EXCHANGE_TEMPLATES.map((template) => (
                      <ExchangeCard
                        key={template.exchange_type}
                        template={template}
                        selected={selectedExchangeType === template.exchange_type}
                        onClick={() => handleSelectExchange(template.exchange_type)}
                        disabled={webCryptoStatus !== 'secure' && webCryptoStatus !== 'disabled'}
                      />
                    ))}
                  </div>
                </div>
              </div>
            </div>
          )}

          {/* Step 1: Configure */}
          {(currentStep === 1 || editingExchangeId) && selectedTemplate && (
            <form onSubmit={handleSubmit} className="space-y-5">
              {/* Selected Exchange Header */}
              <div className="p-4 rounded-xl flex items-center gap-4" style={{ background: '#0B0E11', border: '1px solid #2B3139' }}>
                {getExchangeIcon(selectedTemplate.exchange_type, { width: 48, height: 48 })}
                <div className="flex-1">
                  <div className="font-semibold text-lg" style={{ color: '#EAECEF' }}>
                    {getShortName(selectedTemplate.name)}
                  </div>
                  <div className="text-xs" style={{ color: '#848E9C' }}>
                    {selectedTemplate.type.toUpperCase()} • {selectedTemplate.exchange_type}
                  </div>
                </div>
              </div>

              {/* Account Name */}
              <div className="space-y-2">
                <label className="flex items-center gap-2 text-sm font-semibold" style={{ color: '#EAECEF' }}>
                  <Key className="w-4 h-4" style={{ color: '#F0B90B' }} />
                  {t('exchangeConfig.accountName', language)} *
                </label>
                <input
                  type="text"
                  value={accountName}
                  onChange={(e) => setAccountName(e.target.value)}
                  placeholder={t('exchangeConfig.accountNamePlaceholder', language)}
                  className="w-full px-4 py-3 rounded-xl text-base"
                  style={{ background: '#0B0E11', border: '1px solid #2B3139', color: '#EAECEF' }}
                  required
                />
              </div>

              {/* Hyperliquid Wallet Authorization */}
              {currentExchangeType === 'hyperliquid' && (
                <div className="space-y-4">
                  <div className="p-4 rounded-xl" style={{ background: 'rgba(127, 231, 204, 0.1)', border: '1px solid rgba(127, 231, 204, 0.3)' }}>
                    <div className="flex items-start gap-2">
                      <span style={{ fontSize: '16px' }}>🔐</span>
                      <div>
                        <div className="text-sm font-semibold mb-1" style={{ color: '#7FE7CC' }}>
                          {language === 'zh' ? 'Hyperliquid 必须走钱包授权' : 'Hyperliquid requires wallet authorization'}
                        </div>
                        <div className="text-xs leading-5" style={{ color: '#848E9C' }}>
                          {language === 'zh'
                            ? '不支持手动填写私钥/API Key。请用 MetaMask、Rabby、OKX、Coinbase Wallet 等 EVM 钱包完成连接、Agent 授权和 Builder fee 授权。'
                            : 'Manual private-key/API-key entry is disabled. Use MetaMask, Rabby, OKX, Coinbase Wallet or another EVM wallet to connect, authorize the agent, and approve the builder fee.'}
                        </div>
                      </div>
                    </div>
                  </div>
                  <div className="flex justify-start">
                    <HyperliquidWalletConnect language={language} isLoggedIn={Boolean(user)} variant="inline" />
                  </div>
                </div>
              )}

              {/* Buttons */}
              <div className="flex gap-3 pt-4">
                <button type="button" onClick={handleBack} className="flex-1 px-4 py-3 rounded-xl text-sm font-semibold transition-all hover:bg-white/5" style={{ background: '#2B3139', color: '#848E9C' }}>
                  {currentExchangeType === 'hyperliquid' ? t('closeGuide', language) : editingExchangeId ? t('cancel', language) : t('exchangeConfig.back', language)}
                </button>
              </div>
            </form>
          )}
        </div>
      </div>
    </div>
  )
}
