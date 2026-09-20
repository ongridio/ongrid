import { useEffect, useState } from 'react';
import { getAutoAPMOptions, type AutoAPMOptions } from '@/api/integrations';
import { listDevices } from '@/api/devices';
import { listAllNodes } from '@/api/topology';

export function useAutoAPMOptions() {
  const [options, setOptions] = useState<AutoAPMOptions>({ environments: [], namespaces: [] });
  const [error, setError] = useState('');
  useEffect(() => {
    let cancelled = false;
    void Promise.all([getAutoAPMOptions(), listAllNodes('cluster'), listDevices()]).then(([saved, clusters, devices]) => {
      if (cancelled) return;
      setOptions({
        namespaces: saved.namespaces,
        environments: [...new Set([...saved.environments, ...devices.items.flatMap(device => device.environment ? [device.environment] : []), ...clusters.flatMap(cluster => typeof cluster.props?.environment === 'string' && cluster.props.environment ? [cluster.props.environment] : [])])].sort(),
      });
    }).catch((e: Error) => { if (!cancelled) setError(e.message); });
    return () => { cancelled = true; };
  }, []);
  return { options, error };
}
