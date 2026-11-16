import type { Settings, SettingsForm } from "@ksb/api-client";
import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api, failure } from "./api";

// Пока книги ждут проверки, ход фонового поиска опрашивается.
const progressPoll = 10_000;

export const settingsQuery = queryOptions({
  queryKey: ["settings"],
  refetchInterval: (q) => (q.state.data?.metadata_suggestions.pending ? progressPoll : false),
  queryFn: async (): Promise<Settings> => {
    const { data, error, response } = await api.GET("/api/v1/settings");
    if (error) throw failure(error, response);
    return data;
  },
});

export function useSaveSettings() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (body: SettingsForm): Promise<Settings> => {
      const { data, error, response } = await api.PUT("/api/v1/settings", { body });
      if (error) throw failure(error, response);
      return data;
    },
    onSuccess: (settings) => client.setQueryData(settingsQuery.queryKey, settings),
  });
}
