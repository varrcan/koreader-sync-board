-- Эталон разбора ленты для golden.json.
--
-- Разбирает testdata/feed.xml настоящими plugins/opds.koplugin/opdsparser.lua и
-- frontend/luxl.lua из koreader/koreader, коммит 01ccbd790d6269301ad523e75c45f62d022a6d7b;
-- файлы скачивает goldengen. Результат — таблица, которую читает
-- OPDSBrowser:genItemTableFromCatalog, в виде JSON с упорядоченными ключами.
--
-- Запуск: luajit oracle.lua <каталог с opdsparser.lua и luxl.lua> <feed.xml>

package.path = arg[1] .. "/?.lua;" .. package.path

-- От util парсеру нужна только эта функция, а сам util тянет весь фронтенд KOReader.
package.loaded["util"] = {
    unicodeCodepointToUtf8 = function(c)
        local bit = require("bit")
        local function tail(v) return 0x80 + bit.band(v, 0x3F) end
        if c < 0x80 then
            return string.char(c)
        elseif c < 0x800 then
            return string.char(0xC0 + bit.rshift(c, 6), tail(c))
        elseif c < 0x10000 then
            return string.char(0xE0 + bit.rshift(c, 12), tail(bit.rshift(c, 6)), tail(c))
        end
        return string.char(0xF0 + bit.rshift(c, 18), tail(bit.rshift(c, 12)), tail(bit.rshift(c, 6)), tail(c))
    end,
}

local OPDSParser = require("opdsparser")

local function quote(s)
    return '"' .. s:gsub('[%c"\\]', function(ch)
        if ch == '"' then return '\\"' end
        if ch == "\\" then return "\\\\" end
        return string.format("\\u%04x", ch:byte())
    end) .. '"'
end

local function encode(v, indent)
    if type(v) ~= "table" then
        return quote(tostring(v))
    end
    local inner = indent .. "  "
    local parts = {}
    if #v > 0 then
        for _, item in ipairs(v) do
            table.insert(parts, inner .. encode(item, inner))
        end
        return "[\n" .. table.concat(parts, ",\n") .. "\n" .. indent .. "]"
    end
    local keys = {}
    for k in pairs(v) do table.insert(keys, k) end
    table.sort(keys)
    for _, k in ipairs(keys) do
        table.insert(parts, inner .. quote(k) .. ": " .. encode(v[k], inner))
    end
    if #parts == 0 then return "{}" end
    return "{\n" .. table.concat(parts, ",\n") .. "\n" .. indent .. "}"
end

local file = assert(io.open(arg[2], "rb"))
local text = file:read("*a")
file:close()
io.stdout:write(encode(OPDSParser:parse(text), ""), "\n")
