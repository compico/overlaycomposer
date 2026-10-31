// Сопряжённые устройства (планшет, телефон): код сопряжения и список. Доступно только с самого компьютера.
import type { Api } from "../../lib/api.ts";
import { createStore, type Readable } from "../../lib/store.ts";

export interface DevicesInfo {
  code: string;
  expiresIn: number;
  devices: { id: string; name: string; createdAt: string; lastSeen: string; lastIp: string }[];
  remoteEnabled: boolean;
  pairUrl: string;
}

export interface DevicesModel extends Readable<DevicesInfo | null> {
  /** null в состоянии — раздел недоступен (403 с других компьютеров). */
  load(): Promise<void>;
  revoke(id: string): Promise<void>;
}

export function createDevicesModel(api: Api): DevicesModel {
  const state = createStore<DevicesInfo | null>(null);
  const model: DevicesModel = {
    get: state.get,
    subscribe: state.subscribe,
    async load() {
      try { state.set(await api<DevicesInfo>("/api/devices")); } catch { state.set(null); }
    },
    async revoke(id) {
      await api(`/api/devices/${encodeURIComponent(id)}`, "DELETE");
      await model.load();
    },
  };
  return model;
}
