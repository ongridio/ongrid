import { useEffect, useState } from 'react';
import { getAutoAPMOptions, type AutoAPMOptions } from '@/api/integrations';
import { listAllNodes } from '@/api/topology';

export function useAutoAPMOptions() {
  const [options, setOptions] = useState<AutoAPMOptions>({ environments: [], namespaces: [] });
  const [error, setError] = useState('');
  useEffect(() => {
    let cancelled = false;
    void Promise.all([getAutoAPMOptions(), listAllNodes('cluster')]).then(([saved, clusters]) => {
      if (cancelled) return;
      setOptions({
        namespaces: saved.namespaces,
        environments: [...new Set([...saved.environments, ...clusters.flatMap(cluster => typeof cluster.props?.environment === 'string' && cluster.props.environment ? [cluster.props.environment] : [])])].sort(),
      });
    }).catch((e: Error) => { if (!cancelled) setError(e.message); });
    return () => { cancelled = true; };
  }, []);
  return { options, error };
}
