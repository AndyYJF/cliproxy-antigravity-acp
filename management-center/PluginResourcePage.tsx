import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { EmptyState } from '@/components/ui/EmptyState';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { apiClient } from '@/services/api/client';
import {
  ACP_CHANNEL,
  isACPFrameMessage,
  resolveACPAction,
  resolveACPPayload,
} from './acpManagementBridge';
import { pluginsApi } from '@/services/api';
import { useAuthStore, useThemeStore } from '@/stores';
import { getErrorMessage, isRecord } from '@/utils/helpers';
import type { PluginListResponse } from '@/types';
import {
  collectPluginResourceEntries,
  PLUGIN_RESOURCES_REFRESH_EVENT,
  resolvePluginAssetURL,
} from './pluginResources';
import styles from './PluginResourcePage.module.scss';

const hasStatus = (error: unknown, status: number) => isRecord(error) && error.status === status;

const safeDecodeURIComponent = (value = '') => {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
};

const parseMenuIndex = (value = '') => {
  const index = Number.parseInt(value, 10);
  return Number.isInteger(index) && index >= 0 ? index : -1;
};

export function PluginResourcePage() {
  const { t } = useTranslation();
  const params = useParams<{ pluginId: string; menuIndex: string }>();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const apiBase = useAuthStore((state) => state.apiBase);
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const theme = useThemeStore((state) => state.theme);
  const frameTheme = resolvedTheme === 'dark' ? 'dark' : theme === 'light' ? 'light' : 'white';

  const [data, setData] = useState<PluginListResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const connected = connectionStatus === 'connected';
  const pluginID = useMemo(() => safeDecodeURIComponent(params.pluginId), [params.pluginId]);
  const menuIndex = useMemo(() => parseMenuIndex(params.menuIndex), [params.menuIndex]);

  const loadResource = useCallback(async () => {
    if (!connected) {
      setLoading(false);
      setError(t('notification.connection_required'));
      return;
    }

    setLoading(true);
    setError('');
    try {
      const plugins = await pluginsApi.list();
      setData(plugins);
    } catch (err: unknown) {
      setError(
        hasStatus(err, 404)
          ? t('plugin_management.unsupported_backend')
          : getErrorMessage(err, t('plugin_resource.load_failed'))
      );
    } finally {
      setLoading(false);
    }
  }, [connected, t]);

  useHeaderRefresh(loadResource, connected);

  useEffect(() => {
    void loadResource();
  }, [loadResource]);

  useEffect(() => {
    window.addEventListener(PLUGIN_RESOURCES_REFRESH_EVENT, loadResource);

    return () => {
      window.removeEventListener(PLUGIN_RESOURCES_REFRESH_EVENT, loadResource);
    };
  }, [loadResource]);

  const resource = useMemo(() => {
    const entries = collectPluginResourceEntries(data?.plugins ?? []);
    return entries.find((entry) => entry.pluginID === pluginID && entry.menuIndex === menuIndex);
  }, [data?.plugins, menuIndex, pluginID]);

  const iframeSrc = resource ? resolvePluginAssetURL(resource.menu.path, apiBase) : '';
  const frameRef = useRef<HTMLIFrameElement>(null);

  useEffect(() => {
    if (!connected || pluginID !== 'antigravity-acp' || !iframeSrc) return;
    const origin = new URL(iframeSrc, window.location.href).origin;
    const revision = apiClient.getConnectionRevision();
    let active = true;
    const controller = new AbortController();
    const client = apiClient.captureConnection(() => active);
    const onMessage = async (event: MessageEvent<unknown>) => {
      if (
        !isACPFrameMessage(
          pluginID,
          event.source,
          frameRef.current?.contentWindow,
          event.origin,
          origin
        )
      )
        return;
      if (!isRecord(event.data) || event.data.channel !== ACP_CHANNEL) return;
      const frame = frameRef.current?.contentWindow;
      if (event.data.type === 'ready') {
        frame?.postMessage({ channel: ACP_CHANNEL, type: 'connected', theme: frameTheme }, origin);
        return;
      }
      const operation = resolveACPAction(event.data.action);
      const id = event.data.id;
      if (event.data.type !== 'request' || !operation || typeof id !== 'string' || id.length > 100)
        return;
      const body = resolveACPPayload(event.data.action, event.data.body);
      if (body === null) return;
      try {
        const url = apiBase.replace(/\/$/, '') + operation.path;
        const options = { signal: controller.signal, timeout: 45000 };
        const data =
          operation.method === 'GET'
            ? await client.get(url, options)
            : await client.post(url, body, options);
        if (active && revision === apiClient.getConnectionRevision()) {
          frame?.postMessage(
            { channel: ACP_CHANNEL, type: 'response', id, ok: true, data },
            origin
          );
        }
      } catch (error: unknown) {
        if (active && revision === apiClient.getConnectionRevision()) {
          frame?.postMessage(
            {
              channel: ACP_CHANNEL,
              type: 'response',
              id,
              ok: false,
              error: getErrorMessage(error),
            },
            origin
          );
        }
      }
    };
    frameRef.current?.contentWindow?.postMessage(
      { channel: ACP_CHANNEL, type: 'theme', theme: frameTheme },
      origin
    );
    window.addEventListener('message', onMessage);
    return () => {
      active = false;
      controller.abort();
      window.removeEventListener('message', onMessage);
    };
  }, [apiBase, connected, frameTheme, iframeSrc, pluginID]);

  return (
    <div className={styles.page}>
      {loading ? (
        <div className={styles.stateShell}>
          <div className={styles.statusPanel}>{t('common.loading')}</div>
        </div>
      ) : error ? (
        <div className={styles.stateShell}>
          <EmptyState title={t('plugin_resource.unavailable')} description={error} />
        </div>
      ) : !resource ? (
        <div className={styles.stateShell}>
          <EmptyState
            title={t('plugin_resource.not_found')}
            description={t('plugin_resource.not_found_desc')}
          />
        </div>
      ) : !iframeSrc ? (
        <div className={styles.stateShell}>
          <EmptyState
            title={t('plugin_resource.empty_src')}
            description={t('plugin_resource.empty_src_desc')}
          />
        </div>
      ) : (
        <iframe
          ref={frameRef}
          className={styles.frame}
          src={iframeSrc}
          title={resource.label}
          referrerPolicy="no-referrer"
          allow="clipboard-read; clipboard-write"
        />
      )}
    </div>
  );
}
