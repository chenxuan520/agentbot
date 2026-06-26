import unittest
from importlib import util
from pathlib import Path


spec = util.spec_from_file_location("mention_after_reply", Path(__file__).with_name("mention_after_reply.py"))
hook = util.module_from_spec(spec)
assert spec is not None and spec.loader is not None
spec.loader.exec_module(hook)


class MentionAfterReplyTest(unittest.TestCase):
    def setUp(self) -> None:
        self.members = [
            {"name": "凌晨", "memberId": "ou-1"},
            {"name": "张玉玉", "memberId": "ou-2"},
            {"name": "刘若曼", "memberId": "ou-3"},
        ]

    def test_match_member_names_requires_boundary(self) -> None:
        self.assertEqual(hook.match_member_names_in_reply("@凌晨张玉玉", self.members), [])

    def test_match_member_names_rejects_non_boundary_suffix(self) -> None:
        self.assertEqual(hook.match_member_names_in_reply("@凌晨张玉玉abc", self.members), [])

    def test_replace_mentions_keeps_repeated_mentions(self) -> None:
        self.assertEqual(
            hook.replace_mentions_with_md_at("@凌晨 @张玉玉 @凌晨", ["凌晨", "张玉玉"], ["ou-1", "ou-2"]),
            '<at user_id="ou-1">凌晨</at> <at user_id="ou-2">张玉玉</at> <at user_id="ou-1">凌晨</at>',
        )

    def test_replace_mentions_does_not_guess_multiple_names_from_single_at(self) -> None:
        self.assertEqual(
            hook.replace_mentions_with_md_at("@凌晨张玉玉", ["凌晨", "张玉玉"], ["ou-1", "ou-2"]),
            "@凌晨张玉玉",
        )

    def test_replace_mentions_removes_code_span_for_single_mention(self) -> None:
        self.assertEqual(
            hook.replace_mentions_with_md_at("`@凌晨`", ["凌晨"], ["ou-1"]),
            '<at user_id="ou-1">凌晨</at>',
        )

    def test_replace_mentions_removes_code_span_for_multiple_mentions(self) -> None:
        self.assertEqual(
            hook.replace_mentions_with_md_at(
                "不用继续 `@凌晨 @张玉玉` 追这条告警。",
                ["凌晨", "张玉玉"],
                ["ou-1", "ou-2"],
            ),
            '不用继续 <at user_id="ou-1">凌晨</at> <at user_id="ou-2">张玉玉</at> 追这条告警。',
        )

    def test_replace_mentions_keeps_code_span_when_non_mention_text_exists(self) -> None:
        self.assertEqual(
            hook.replace_mentions_with_md_at(
                "请保留 `联系 @凌晨 处理` 这段格式。",
                ["凌晨"],
                ["ou-1"],
            ),
            '请保留 `联系 <at user_id="ou-1">凌晨</at> 处理` 这段格式。',
        )


if __name__ == "__main__":
    unittest.main()
