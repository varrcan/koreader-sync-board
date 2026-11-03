-- Эталон сэмплирования partial MD5 для golden.json.
--
-- Цикл скопирован дословно из koreader/koreader, frontend/util.lua, util.partialMD5,
-- коммит 78d586b420835e85cacf4fc72f1a7e7a40741afd:
-- https://github.com/koreader/koreader/blob/78d586b420835e85cacf4fc72f1a7e7a40741afd/frontend/util.lua#L1094-L1111
--
-- Единственное отличие: вместо md5() выбранные байты пишутся в stdout, хеш от них
-- считает md5sum. При обновлении KOReader сверить цикл с актуальным util.lua.
--
-- Запуск: luajit oracle.lua <файл> | md5sum

local bit = require("bit")
local lshift = bit.lshift

local function partialSamples(filepath)
    if not filepath then return end
    local file = io.open(filepath, "rb")
    if not file then return end
    local step, size = 1024, 1024
    for i = -1, 10 do
        file:seek("set", lshift(step, 2*i))
        local sample = file:read(size)
        if sample then
            io.stdout:write(sample)
        else
            break
        end
    end
    file:close()
end

partialSamples(arg[1])
